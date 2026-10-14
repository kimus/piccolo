package runtimeplan

import (
	"testing"
)

func gib(n int64) int64 { return n << 30 }

func TestRetainProtectsTheFloor(t *testing.T) {
	images := []ImageInfo{
		{Ref: "piccolo/picoclaw-runtime:cur", ID: "sha256:a", Size: gib(8), Created: 300},
		{Ref: "piccolo/picoclaw-runtime:prev", ID: "sha256:b", Size: gib(8), Created: 200},
		{Ref: "piccolo/picoclaw-runtime:held", ID: "sha256:c", Size: gib(8), Created: 150, Containers: []string{"piccolo-setup-gws"}},
		{Ref: "piccolo/picoclaw-runtime:old", ID: "sha256:d", Size: gib(8), Created: 100},
	}
	images[0].Containers = []string{"piccolo-gateway"}

	plan := Retain(images, "piccolo/picoclaw-runtime:cur", "piccolo/picoclaw-runtime:prev")

	if len(plan.Removable) != 1 || plan.Removable[0].Ref != "piccolo/picoclaw-runtime:old" {
		t.Fatalf("removable = %#v", plan.Removable)
	}
	reasons := map[string]string{}
	for _, k := range plan.Kept {
		reasons[k.Info.Ref] = k.Reason
	}
	if reasons["piccolo/picoclaw-runtime:cur"] != "in use" {
		t.Errorf("current image reason = %q", reasons["piccolo/picoclaw-runtime:cur"])
	}
	if reasons["piccolo/picoclaw-runtime:prev"] != "rollback target" {
		t.Errorf("rollback reason = %q", reasons["piccolo/picoclaw-runtime:prev"])
	}
	// The stopped one-shot must protect its image by name, not silently.
	if got := reasons["piccolo/picoclaw-runtime:held"]; got != "referenced by container piccolo-setup-gws" {
		t.Errorf("container-referenced reason = %q", got)
	}
	if plan.Reclaimable != gib(8) {
		t.Errorf("reclaimable = %d", plan.Reclaimable)
	}
}

// The rollback target must be protected even when it is also referenced by a
// container: "rollback target" is the reason that matters to the user.
func TestRetainReasonPrecedence(t *testing.T) {
	images := []ImageInfo{
		{Ref: "piccolo/picoclaw-runtime:prev", ID: "sha256:b", Size: gib(1), Containers: []string{"piccolo-gateway.prev"}},
	}
	plan := Retain(images, "piccolo/picoclaw-runtime:cur", "piccolo/picoclaw-runtime:prev")
	if len(plan.Kept) != 1 || plan.Kept[0].Reason != "rollback target" {
		t.Fatalf("kept = %#v", plan.Kept)
	}
}

// A record can be reachable by ID as well as by tag: the manifest may name a
// digest, and a container may reference the image by id.
func TestRetainMatchesByIDToo(t *testing.T) {
	images := []ImageInfo{{Ref: "piccolo/picoclaw-runtime:x", ID: "sha256:deadbeef", Size: gib(1)}}
	plan := Retain(images, "sha256:deadbeef", "")
	if len(plan.Removable) != 0 || len(plan.Kept) != 1 {
		t.Fatalf("by-id match failed: %#v", plan)
	}
}

// No rollback recorded (first promotion ever) must not remove the running image,
// and must not treat the only image as free space.
func TestRetainWithoutHistory(t *testing.T) {
	images := []ImageInfo{
		{Ref: "piccolo/picoclaw-runtime:only", Size: gib(1), Containers: []string{"piccolo-gateway"}},
		{Ref: "piccolo/picoclaw-runtime:stale", Size: gib(1)},
	}
	plan := Retain(images, "piccolo/picoclaw-runtime:only", "")
	if len(plan.Removable) != 1 || plan.Removable[0].Ref != "piccolo/picoclaw-runtime:stale" {
		t.Fatalf("removable = %#v", plan.Removable)
	}
}

// Newest first: the report should read like a changelog, not a hash dump.
func TestRetainOrdersNewestFirst(t *testing.T) {
	images := []ImageInfo{
		{Ref: "piccolo/picoclaw-runtime:old", Size: 1, Created: 10},
		{Ref: "piccolo/picoclaw-runtime:new", Size: 1, Created: 30},
		{Ref: "piccolo/picoclaw-runtime:mid", Size: 1, Created: 20},
	}
	plan := Retain(images, "", "")
	got := []string{}
	for _, r := range plan.Removable {
		got = append(got, r.Ref)
	}
	want := "piccolo/picoclaw-runtime:new piccolo/picoclaw-runtime:mid piccolo/picoclaw-runtime:old"
	if join(got) != want {
		t.Errorf("order = %s, want %s", join(got), want)
	}
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

func TestRuntimeImagePrefix(t *testing.T) {
	if !RuntimeImagePrefix("piccolo/picoclaw-runtime:abc", "piccolo/picoclaw-runtime") {
		t.Error("own image not recognised")
	}
	if RuntimeImagePrefix("docker.io/sipeed/picoclaw:launcher", "piccolo/picoclaw-runtime") {
		t.Error("base image must never be a reclamation candidate")
	}
	if RuntimeImagePrefix("piccolo/picoclaw-runtimey:abc", "piccolo/picoclaw-runtime") {
		t.Error("prefix must be a complete repository name")
	}
}
