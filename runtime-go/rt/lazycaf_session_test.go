package rt

import "testing"

// A CAF first forced inside a Sky.Live session must not belong to it: its
// computation sees no session stamp, and the caller's stamp is restored after.
func TestLazyCaf_ComputesOutsideTheForcingSession(t *testing.T) {
	sess := &liveSession{}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	var cell LazyCaf[bool]
	sawSession := cell.Get(func() bool { return currentLiveSession() != nil })
	if sawSession {
		t.Fatal("the CAF computation ran with the forcing session's stamp; a handle it opens would be owned by that session")
	}
	if currentLiveSession() != sess {
		t.Fatal("the caller's session stamp must be restored after the CAF is forced")
	}
}

// Outside any session nothing changes.
func TestLazyCaf_NoSessionIsUnchanged(t *testing.T) {
	clearGoroutineLiveSession()
	var cell LazyCaf[int]
	calls := 0
	for i := 0; i < 3; i++ {
		if v := cell.Get(func() int { calls++; return 7 }); v != 7 {
			t.Fatalf("got %d", v)
		}
	}
	if calls != 1 || currentLiveSession() != nil {
		t.Fatalf("computed %d times, stamp %v", calls, currentLiveSession())
	}
}
