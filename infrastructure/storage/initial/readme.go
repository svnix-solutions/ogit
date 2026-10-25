// Package initial holds what a new repository starts with, shared by the
// storage backends.
package initial

// Readme is the README.md of a new repository's first commit, titled with
// the repository's name.
func Readme(name string) []byte {
	return []byte("# " + name + "\n")
}
