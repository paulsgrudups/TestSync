package runs

import "testing"

func TestEnsureTestAndGetTest(t *testing.T) {
	AllTests = make(map[int]*Test)

	created := EnsureTest(10, func() *Test {
		return &Test{}
	})
	if created == nil {
		t.Fatal("expected created test, got nil")
	}

	second := EnsureTest(10, func() *Test {
		return &Test{}
	})
	if created != second {
		t.Fatal("expected EnsureTest to return existing test")
	}

	got, ok := GetTest(10)
	if !ok || got != created {
		t.Fatal("expected GetTest to return created test")
	}
}

func TestDeleteTest(t *testing.T) {
	AllTests = make(map[int]*Test)
	SetTest(5, &Test{})

	if _, ok := GetTest(5); !ok {
		t.Fatal("expected test to exist before delete")
	}

	DeleteTest(5)
	if _, ok := GetTest(5); ok {
		t.Fatal("expected test to be deleted")
	}
}
