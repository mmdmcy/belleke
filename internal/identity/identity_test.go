package identity

import "testing"

func TestLoadMakesOneKeyAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID() != b.ID() {
		t.Fatal("a second load made a different key")
	}
	pub, err := Decode(a.ID())
	if err != nil || !Verify(pub, []byte("hi"), a.Sign([]byte("hi"))) {
		t.Fatal("round trip of the public key failed")
	}
	if _, err := Decode("nope"); err == nil {
		t.Fatal("garbage decoded")
	}
}

func TestFingerprintIsSymmetricAndShort(t *testing.T) {
	a, _ := Load(t.TempDir())
	b, _ := Load(t.TempDir())
	if Fingerprint(a.Public, b.Public) != Fingerprint(b.Public, a.Public) {
		t.Fatal("order mattered")
	}
	if len(Fingerprint(a.Public, b.Public)) != 7 {
		t.Fatal("not six digits")
	}
	if Fingerprint(a.Public, b.Public) == Fingerprint(a.Public, a.Public) {
		t.Fatal("different pairs, same code")
	}
}
