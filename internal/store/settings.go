package store

// Simple key/value settings (v2.0). Email alert configuration is stored
// here; the SMTP password lives only in this local database and is never
// rendered back in full by the web API (masked — see internal/web).

func (s *Store) GetSetting(key string) (string, bool) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v, err == nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// GetSettings returns several settings at once; missing keys are absent.
func (s *Store) GetSettings(keys ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.GetSetting(k); ok {
			out[k] = v
		}
	}
	return out, nil
}
