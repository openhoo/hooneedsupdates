package update

import "testing"

func TestNormalizeAndClassifyVersion(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		kind    string
	}{
		{"1.2.3", "1.2.4", "patch"},
		{"^1.2", "1.3.0", "minor"},
		{"v1", "v2.0.0", "major"},
	}
	for _, test := range tests {
		if !newer(test.current, test.latest) {
			t.Fatalf("expected %s to be newer than %s", test.latest, test.current)
		}
		if got := updateType(test.current, test.latest); got != test.kind {
			t.Fatalf("updateType(%q, %q) = %q, want %q", test.current, test.latest, got, test.kind)
		}
	}
	if normalizeVersion("latest") != "" {
		t.Fatal("non-version tag must not normalize")
	}
}

func TestChooseVersionExcludesPrerelease(t *testing.T) {
	resolution, err := chooseVersion([]string{"v1.0.0", "v2.0.0-rc.1", "v1.4.0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != "v1.4.0" {
		t.Fatalf("got %s", resolution.Version)
	}
}

func TestDockerVersionShapeRejectsDateTags(t *testing.T) {
	if dockerVersionDots("20260805") >= dockerVersionDots("3.22") {
		t.Fatal("date tag must not share dotted release channel")
	}
}

func TestChooseGoVersionAcceptsPseudoVersions(t *testing.T) {
	want := "v0.0.0-20260801000000-0123456789ab"
	resolution, err := chooseGoVersion([]string{"v0.0.0-20250801000000-0123456789ab", want}, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Version != want {
		t.Fatalf("got %s, want %s", resolution.Version, want)
	}
}

func TestCargoConstraintAllowsCompatibleLatest(t *testing.T) {
	tests := []struct {
		requirement string
		latest      string
		allowed     bool
	}{
		{"1.0", "1.9.0", true},
		{"1.0", "2.0.0", false},
		{"0.8", "0.8.9", true},
		{"0.8", "0.9.0", false},
		{"~1.2", "1.2.9", true},
		{"~1.2", "1.3.0", false},
		{"=1.2.3", "1.2.4", false},
	}
	for _, test := range tests {
		candidate := Candidate{Manager: ManagerCargo, CurrentValue: test.requirement}
		if got := constraintAllowsLatest(candidate, test.latest); got != test.allowed {
			t.Fatalf("constraintAllowsLatest(%q, %q) = %v, want %v", test.requirement, test.latest, got, test.allowed)
		}
	}
}
