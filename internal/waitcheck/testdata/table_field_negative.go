package p

func TestA(t *testing.T) {
	tests := []struct{ run func() }{{run: func() { time.Sleep(time.Second) }}}
	for _, tt := range tests {
		synctest.Test(t, func(t *testing.T) { tt.run() })
	}
}
