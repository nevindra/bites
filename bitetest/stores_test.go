package bitetest_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// fkStore is an OwnerSessionStore that behaves like a sessions table whose owner column refers to
// a users table: it refuses an owner that is not in users, and an empty one.
type fkStore struct {
	mu      sync.Mutex
	users   map[string]bool
	data    map[string][]byte
	expires map[string]time.Time
	owner   map[string]string
}

func newFKStore() *fkStore {
	return &fkStore{users: map[string]bool{}, data: map[string][]byte{}, expires: map[string]time.Time{}, owner: map[string]string{}}
}

func (f *fkStore) addUser(u string) { f.mu.Lock(); f.users[u] = true; f.mu.Unlock() }

func (f *fkStore) Load(ctx context.Context, id string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[id]
	if !ok || !f.expires[id].After(bites.Now(ctx)) {
		return nil, false, nil
	}
	return d, true, nil
}

func (f *fkStore) Save(ctx context.Context, id string, data []byte, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[id], f.expires[id] = data, expires
	delete(f.owner, id)
	return nil
}

func (f *fkStore) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
	delete(f.owner, id)
	return nil
}

func (f *fkStore) SaveOwned(ctx context.Context, id, owner string, data []byte, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.users[owner] {
		return errors.New("foreign key violation: no user " + owner)
	}
	f.data[id], f.expires[id], f.owner[id] = data, expires, owner
	return nil
}

func (f *fkStore) DeleteOwner(ctx context.Context, owner, keep string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, o := range f.owner {
		if o == owner && id != keep {
			delete(f.data, id)
			delete(f.owner, id)
			n++
		}
	}
	return n, nil
}

func TestAnOwnerSessionStoreWhoseOwnersAreRealRowsPassesWhenTheSuiteCreatesThem(t *testing.T) {
	var asked sync.Map
	bitetest.TestOwnerSessionStore(t, func(t *testing.T) bites.OwnerSessionStore { return newFKStore() }, bitetest.OwnerSessionSuite{
		Owner: func(t *testing.T, s bites.OwnerSessionStore, name string) string {
			asked.Store(name, true)
			s.(*fkStore).addUser("user:" + name)
			return "user:" + name
		},
		NoEmptyOwner: true,
	})
	for _, name := range []string{"alice", "bob", "nobody", "Alice", "alice ", "ali", "alice@example.com", "café"} {
		if _, ok := asked.Load(name); !ok {
			t.Errorf("Owner was never asked for %q", name)
		}
	}
}

func TestAnEmptyOwnerSubtestIsSkippedWithItsReasonWhenNoEmptyOwnerIsSet(t *testing.T) {
	out := runChild(t, "TestChildFKStoreSuite")
	if !strings.Contains(out, "NoEmptyOwner is set") || !strings.Contains(out, "SKIP") {
		t.Fatalf("the skip did not say why in the output:\n%s", out)
	}
}

func TestChildFKStoreSuite(t *testing.T) {
	if os.Getenv("BITETEST_CHILD") == "" {
		t.Skip("run by its parent")
	}
	bitetest.TestOwnerSessionStore(t, func(t *testing.T) bites.OwnerSessionStore { return newFKStore() }, bitetest.OwnerSessionSuite{
		Owner: func(t *testing.T, s bites.OwnerSessionStore, name string) string {
			s.(*fkStore).addUser(name)
			return name
		},
		NoEmptyOwner: true,
	})
}

func TestAnOwnerSessionStoreThatRefusesOwnersFailsTheSuiteWithoutOwnerOrWithEmptyOwners(t *testing.T) {
	out := runChild(t, "TestChildFKStoreWithoutOwner")
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "foreign key violation") {
		t.Fatalf("a store that refuses unknown owners did not fail the default suite:\n%s", out)
	}
}

func TestChildFKStoreWithoutOwner(t *testing.T) {
	if os.Getenv("BITETEST_CHILD") == "" {
		t.Skip("run by its parent")
	}
	bitetest.TestOwnerSessionStore(t, func(t *testing.T) bites.OwnerSessionStore { return newFKStore() })
}

func TestAnOwnerThatIsEmptyOrNotDistinctFailsTheSuiteSayingWhatToChange(t *testing.T) {
	for _, tc := range []struct{ child, want string }{
		{"TestChildEmptyOwner", `returned an empty owner for "alice": return the id of a real owner`},
		{"TestChildSameOwner", `returned "x" for both "alice" and "bob": return a different owner for each name`},
		{"TestChildTwoSuites", `2 OwnerSessionSuite values given: give one`},
	} {
		out := runChild(t, tc.child)
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: the output lacks %q:\n%s", tc.child, tc.want, out)
		}
	}
}

func okStore(t *testing.T) bites.OwnerSessionStore { return newFKStore() }

func TestChildEmptyOwner(t *testing.T) {
	if os.Getenv("BITETEST_CHILD") == "" {
		t.Skip("run by its parent")
	}
	bitetest.TestOwnerSessionStore(t, okStore, bitetest.OwnerSessionSuite{
		Owner: func(*testing.T, bites.OwnerSessionStore, string) string { return "" },
	})
}

func TestChildSameOwner(t *testing.T) {
	if os.Getenv("BITETEST_CHILD") == "" {
		t.Skip("run by its parent")
	}
	bitetest.TestOwnerSessionStore(t, okStore, bitetest.OwnerSessionSuite{
		Owner: func(_ *testing.T, s bites.OwnerSessionStore, _ string) string {
			s.(*fkStore).addUser("x")
			return "x"
		},
	})
}

func TestChildTwoSuites(t *testing.T) {
	if os.Getenv("BITETEST_CHILD") == "" {
		t.Skip("run by its parent")
	}
	bitetest.TestOwnerSessionStore(t, okStore, bitetest.OwnerSessionSuite{}, bitetest.OwnerSessionSuite{})
}

// runChild runs one test of this package in a new process, whose failure or skip is the thing
// under test, and returns its combined verbose output.
func runChild(t *testing.T, name string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), "BITETEST_CHILD=1")
	out, _ := cmd.CombinedOutput()
	return string(out)
}
