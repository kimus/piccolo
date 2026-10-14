package runtimeplan

import (
	"fmt"
	"sort"
	"strings"
)

// ImageInfo is one local runtime image with exactly what a reclamation decision
// needs: its size, and the containers (running OR stopped) created from it.
type ImageInfo struct {
	Ref        string
	ID         string
	Size       int64
	Created    int64
	Containers []string
}

// KeptImage records not just that an image survived but why, so the report can
// be checked against intent rather than trusted.
type KeptImage struct {
	Info   ImageInfo
	Reason string
}

// Retention is a reclamation decision over one repository's images.
type Retention struct {
	Scanned     int
	Removable   []ImageInfo
	Kept        []KeptImage
	Reclaimable int64
}

// Retain decides which images may go. The floor is ordered so the first reason
// a protected image matches is the one reported:
//
//  1. the image in use — removing it would break the running gateway's restart
//  2. the recorded rollback target — the one image nothing else can recover
//  3. any container reference, running or stopped — a stopped container is a
//     live reference, and deleting its image strands it
//
// Anything else is unreferenced by definition and fair to reclaim. Sizes are
// summed as an upper bound: layers are shared between tags, so the space a
// removal actually frees is usually less and is only known from the daemon.
func Retain(images []ImageInfo, current, previous string) Retention {
	current = strings.TrimSpace(current)
	previous = strings.TrimSpace(previous)
	out := Retention{Scanned: len(images)}

	given := append([]ImageInfo(nil), images...)
	sort.Slice(given, func(i, j int) bool {
		if given[i].Created == given[j].Created {
			return given[i].Ref < given[j].Ref
		}
		return given[i].Created > given[j].Created
	})

	for _, img := range given {
		switch {
		case current != "" && (img.Ref == current || img.ID == current):
			out.Kept = append(out.Kept, KeptImage{Info: img, Reason: "in use"})
		case previous != "" && (img.Ref == previous || img.ID == previous):
			out.Kept = append(out.Kept, KeptImage{Info: img, Reason: "rollback target"})
		case len(img.Containers) > 0:
			out.Kept = append(out.Kept, KeptImage{Info: img, Reason: "referenced by " + describeContainers(img.Containers)})
		default:
			out.Removable = append(out.Removable, img)
			out.Reclaimable += img.Size
		}
	}
	return out
}

func describeContainers(names []string) string {
	cleaned := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.TrimPrefix(strings.TrimSpace(n), "/"); n != "" {
			cleaned = append(cleaned, n)
		}
	}
	sort.Strings(cleaned)
	switch len(cleaned) {
	case 0:
		return "a container"
	case 1:
		return "container " + cleaned[0]
	default:
		return fmt.Sprintf("container %s (+%d more)", cleaned[0], len(cleaned)-1)
	}
}

// RuntimeImagePrefix reports whether a tag belongs to images Piccolo built,
// which is the only set reclamation may consider.
func RuntimeImagePrefix(ref, prefix string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), strings.TrimSpace(prefix)+":")
}
