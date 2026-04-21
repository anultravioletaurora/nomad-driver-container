module github.com/hashicorp/nomad-driver-container

go 1.22

require (
	github.com/hashicorp/go-hclog v1.6.3
	github.com/hashicorp/go-plugin v1.6.3
	github.com/hashicorp/nomad v1.11.3
)

// NOTE: When importing github.com/hashicorp/nomad you may need additional
// replace directives to satisfy transitive dependencies.  Consult Nomad's own
// go.mod (https://github.com/hashicorp/nomad/blob/main/go.mod) for the
// full list of replace stanzas required by the version you are targeting and
// mirror them here.
//
// Run `go mod tidy` after cloning to resolve the dependency graph.
