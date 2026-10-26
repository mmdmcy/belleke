package audio

import "testing"

func TestJitterStartsAfterTargetAndKeepsOrder(t *testing.T) {
	j := NewJitter(3, 10)
	frames := Tone(440, 6)
	j.Put(2, frames[1])
	j.Put(1, frames[0])
	if j.Get() != nil {
		t.Fatal("played before the target was reached")
	}
	j.Put(3, frames[2])
	if got := j.Get(); got == nil || string(got) != string(frames[0]) {
		t.Fatal("first frame out of order")
	}
	j.Put(5, frames[4]) // 4 is missing
	if string(j.Get()) != string(frames[1]) || string(j.Get()) != string(frames[2]) {
		t.Fatal("order broke")
	}
	if j.Get() != nil || j.Lost != 1 {
		t.Fatal("a missing frame should be one silence and counted lost")
	}
	if string(j.Get()) != string(frames[4]) {
		t.Fatal("did not continue after the gap")
	}
	j.Put(4, frames[3])
	if j.Late != 1 {
		t.Fatal("a late frame should be counted and ignored")
	}
}

func TestJitterDropsWhenFarBehind(t *testing.T) {
	j := NewJitter(2, 4)
	for seq := uint64(1); seq <= 9; seq++ {
		j.Put(seq, []byte{byte(seq)})
	}
	if j.Dropped == 0 {
		t.Fatal("nothing dropped although 9 frames were queued with Max 4")
	}
	if got := j.Get(); got == nil || got[0] < 5 {
		t.Fatalf("still playing old sound: %v", got)
	}
}

func TestLevelAndTone(t *testing.T) {
	frames := Tone(440, 2)
	if len(frames[0]) != FrameBytes {
		t.Fatal("frame size")
	}
	if l := Level(frames[0]); l < 0.3 || l > 0.4 {
		t.Fatalf("tone level %v", l)
	}
	if Level(make([]byte, FrameBytes)) != 0 {
		t.Fatal("silence has a level")
	}
}
