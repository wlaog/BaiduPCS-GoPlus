package reconcile

// Progress describes real work in the current phase. Total == 0 means discovery
// is still in progress; callers must not invent a percentage in that case.
type Progress struct {
	Phase   string `json:"phase"`
	Current string `json:"current,omitempty"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Files   int    `json:"files,omitempty"`
	Dirs    int    `json:"dirs,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
}

type ProgressFunc func(Progress)

func (f ProgressFunc) emit(p Progress) {
	if f != nil {
		f(p)
	}
}
