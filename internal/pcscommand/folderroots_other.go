//go:build !windows

package pcscommand

func localFolderRoots() []string { return []string{"/"} }
