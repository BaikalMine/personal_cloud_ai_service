package domain

import "time"

type GenerationOutputSource struct {
	MediaID           int64
	MediaType         string
	MIMEType          string
	SizeBytes         int64
	ContentHash       string
	ExpiresAt         time.Time
	MeasurementCipher []byte
	MeasurementSHA256 string
}

type MediaMeasurementRecord struct {
	MediaID       int64
	SHA256        string
	SizeBytes     int64
	PayloadCipher []byte
}
