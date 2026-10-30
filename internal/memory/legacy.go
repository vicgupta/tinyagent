package memory

// ImportLegacy copies the entries of the legacy JSON store at legacy (the
// pre-markdown per-project file, see StorePath) into the store at path,
// once: only when path doesn't exist yet and legacy holds entries. It never
// changes or deletes legacy, and returns how many entries it copied. Once
// the new store exists (even emptied by --memory-forget all), it never
// imports again.
func ImportLegacy(path, legacy string) (int, error) {
	if legacy == "" || path == "" || Exists(path) || BackendOf(legacy) != backendJSON {
		return 0, nil
	}
	entries, err := loadJSON(legacy)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	n := 0
	err = Update(path, func(cur []Entry) ([]Entry, error) {
		if len(cur) > 0 {
			return nil, errNothing
		}
		n = len(entries)
		return entries, nil
	})
	if err == errNothing {
		return 0, nil
	}
	return n, err
}
