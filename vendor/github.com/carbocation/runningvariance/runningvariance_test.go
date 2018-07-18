package runningvariance

import "testing"

func TestMean(t *testing.T) {
	s := NewRunningStat()
	s.Push(1)
	s.Push(1)
	s.Push(1)
	s.Push(0)
	s.Push(0)
	s.Push(0)

	if actual, expected := s.Mean(), 0.5; expected != actual {
		t.Errorf("Expected %f, got %f", expected, actual)
	}
}

func TestStandardDeviation(t *testing.T) {
	s := NewRunningStat()

	if actual, expected := s.StandardDeviation(), 0.0; expected != actual {
		t.Errorf("Expected %f, got %f", expected, actual)
	}

	s.Push(1)
	s.Push(1)
	s.Push(1)

	if actual, expected := s.StandardDeviation(), 0.0; expected != actual {
		t.Errorf("Expected %f, got %f", expected, actual)
	}
}

func TestNumDataValues(t *testing.T) {
	s := NewRunningStat()
	s.Push(1)

	if actual, expected := s.NumDataValues(), uint(1); expected != actual {
		t.Errorf("Expected %f, got %f", expected, actual)
	}
}
