package doctor

import (
	"context"
	"sync"

	"github.com/kimus/piccolo/internal/docker"
	"github.com/kimus/piccolo/internal/manifest"
)

// dockerFacts is one concurrent read of everything the daemon can say about a
// workspace.
//
// It exists for two reasons. The first is time: the checks used to ask
// sequentially, so a `doctor` run paid roughly ten daemon round trips in a row
// (~150ms against a local socket, and 10x the latency against a remote Docker
// context, where round-trip cost dominates and the daemon does the work in the
// background anyway). The same probes fanned out take the maximum, not the sum —
// measured 1.9x faster against a local daemon for eight concurrent calls.
//
// The second is that the checks now have ONE source. `ContainerExists` and
// `StateByName` were both asked for the same container by two different checks,
// and a check that queries the daemon inside itself cannot be unit-tested
// without the fake Engine interface; a check that reads a struct can.
//
// live says whether the answers are trustworthy. A nil Engine (offline mode:
// tests and serve's config-only preflight) and a daemon that will not answer a
// ping both leave live false, and every engine-dependent check stays silent
// rather than reporting an absence it never observed.
type dockerFacts struct {
	live bool

	imagePresent bool
	imageErr     error

	labels      map[string]string
	labelsFound bool
	labelsErr   error

	containerExists bool
	containerErr    error

	parkedExists bool
	parkedErr    error

	state    docker.State
	stateErr error

	oneShots    []string
	oneShotsErr error

	images             []docker.ImageRecord
	imagesErr          error
	containerImages    map[string][]string
	containerImagesErr error
}

// fetchDockerFacts probes the daemon concurrently and returns one snapshot.
//
// The caller has already pinged: the ping needs nothing from the workspace, so
// Run starts it before reading the files and lets the two overlap, and re-pinging
// here would throw away that overlap. When the ping failed, the probes would all
// fail the same way, and printing eight errors for one dead daemon is noise.
func fetchDockerFacts(ctx context.Context, e Engine, m *manifest.Manifest, pingErr error) dockerFacts {
	f := dockerFacts{}
	if e == nil || m == nil || pingErr != nil {
		return f
	}
	f.live = true

	var wg sync.WaitGroup
	probe := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	probe(func() {
		f.imagePresent, f.imageErr = e.ImageExists(ctx, m.Container.Image)
	})
	// Labels are only worth reading for an image Piccolo built; the drift check
	// owns that judgment, but asking for labels on a registry image is a round
	// trip that can only ever answer "not found".
	if isPiccoloLocalRuntimeImage(m.Container.Image) {
		probe(func() {
			f.labels, f.labelsFound, f.labelsErr = e.ImageLabels(ctx, m.Container.Image)
		})
	}
	probe(func() {
		f.containerExists, f.containerErr = e.ContainerExists(ctx, m.Container.Name)
	})
	probe(func() {
		f.parkedExists, f.parkedErr = e.ContainerExists(ctx, docker.PreviousName(m.Container.Name))
	})
	probe(func() {
		f.state, f.stateErr = e.StateByName(ctx, m.Container.Name)
	})
	probe(func() {
		f.oneShots, f.oneShotsErr = e.ListByPrefix(ctx, docker.SetupContainerPrefix)
	})
	probe(func() {
		f.images, f.imagesErr = e.ListImages(ctx)
	})
	probe(func() {
		f.containerImages, f.containerImagesErr = e.ContainerImages(ctx)
	})
	wg.Wait()
	return f
}

// ImageLabels serves the pre-fetched labels to runtimeplan.Check, which is
// written against a LabelReader rather than the whole Engine so a snapshot can
// stand in for a daemon. The ctx is accepted and ignored: the answer was already
// gathered, and an unverifiable probe must not become a second network call in
// the middle of a check that reads like a pure function.
func (f dockerFacts) ImageLabels(context.Context, string) (map[string]string, bool, error) {
	return f.labels, f.labelsFound, f.labelsErr
}
