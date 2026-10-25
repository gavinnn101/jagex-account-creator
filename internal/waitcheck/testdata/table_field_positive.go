package p

func TestA(t *testing.T) {
	tests := []struct{ run func() }{{run: func() {
		time.Sleep(time.Second) // want sleep
	}}}
	for _, tt := range tests {
		tt.run()
	}
}
