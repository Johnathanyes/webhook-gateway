package delivery

import "testing"

func TestDeliveryStatus(t *testing.T) {
	tests := []struct {
		name        string
		result      attemptResult
		attempt     int
		maxAttempts int
		want        string
	}{
		{"success", attemptResult{succeeded: true}, 1, 16, statusSucceeded},
		{"terminal 4xx dead-letters immediately", attemptResult{retryable: false}, 1, 16, statusDeadLettered},
		{"retryable but exhausted dead-letters", attemptResult{retryable: true}, 16, 16, statusDeadLettered},
		{"retryable with budget left retries", attemptResult{retryable: true}, 3, 16, statusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliveryStatus(tt.result, tt.attempt, tt.maxAttempts); got != tt.want {
				t.Errorf("deliveryStatus = %q, want %q", got, tt.want)
			}
		})
	}
}
