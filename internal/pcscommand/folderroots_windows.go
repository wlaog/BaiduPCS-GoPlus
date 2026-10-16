package pcscommand

import "golang.org/x/sys/windows"

func localFolderRoots() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return []string{`C:\`}
	}
	var roots []string
	for drive := 0; drive < 26; drive++ {
		if mask&(1<<drive) != 0 {
			roots = append(roots, string(rune('A'+drive))+`:\`)
		}
	}
	return roots
}
