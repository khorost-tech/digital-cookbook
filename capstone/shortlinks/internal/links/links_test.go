package links

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/a?b=1": true, "http://x.org": true,
		"": false, "ftp://example.com": false, "javascript:alert(1)": false, "https://": false, "/relative": false,
	} {
		if err := ValidateURL(raw); (err == nil) != ok {
			t.Errorf("%q: ok=%v, err=%v", raw, ok, err)
		}
	}
}

type memStore struct {
	taken int
	links map[string]Link
}

func (m *memStore) Create(_ context.Context, l Link) error {
	if m.taken > 0 {
		m.taken--
		return ErrCodeTaken
	}
	m.links[l.Code] = l
	return nil
}
func (m *memStore) Get(_ context.Context, code string) (Link, error) {
	l, ok := m.links[code]
	if !ok {
		return Link{}, ErrNotFound
	}
	return l, nil
}
func (m *memStore) Clicks(context.Context, string) (int64, error) { return 0, nil }

func TestCreateRetriesTakenCode(t *testing.T) {
	st := &memStore{taken: 2, links: map[string]Link{}}
	s := &Service{Store: st, Now: time.Now}
	l, err := s.Create(context.Background(), "https://example.com")
	if err != nil || len(l.Code) != codeLen {
		t.Fatalf("после двух коллизий ожидалась ссылка: %v %v", l, err)
	}
	st.taken = 3
	if _, err := s.Create(context.Background(), "https://example.com"); !errors.Is(err, ErrCodeTaken) {
		t.Fatalf("после трёх коллизий — ErrCodeTaken, получено %v", err)
	}
}
