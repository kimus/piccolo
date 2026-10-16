// Standalone module ON PURPOSE. It holds the only reference to PicoClaw in this
// repository, and its existence keeps that reference out of Piccolo's dependency
// graph: Go skips any directory containing its own go.mod when resolving the
// parent module's packages. Deleting this file would make the compatibility check
// import PicoClaw into Piccolo's build, which design decision D1 rejects.
module piccolocredentialcompat

go 1.26.0

require github.com/sipeed/picoclaw v0.3.1

require (
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)
