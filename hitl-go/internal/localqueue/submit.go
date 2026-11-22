// hitl-go/internal/localqueue/submit.go
package localqueue

import "context"

// SubmitExtractionJob replaces iqueue.EnqueueExtractionJob. Same signature,
// so documents.go's call site only needs its package qualifier changed.
func SubmitExtractionJob(ctx context.Context, extractionID, pdfPath, schemaID, docHash string) error {
	return Submit(Job{
		TaskType:     "process_document",
		ExtractionID: extractionID,
		PdfPath:      pdfPath,
		SchemaID:     schemaID,
		DocHash:      docHash,
	})
}

// SubmitSingleOCRJob — direct single-PDF mode: OCR runs immediately and any
// pre-supplied corrections (regionsJSON, each with an optional
// "correction_value") are applied straight to the PDF, no review-queue step.
// Matches UploadPage.tsx's directExtractionId/directProcessing flow.
func SubmitSingleOCRJob(ctx context.Context, extractionID, pdfPath, schemaID, docHash, regionsJSON string, serialNumber int) error {
	return Submit(Job{
		TaskType:     "process_single_ocr_document",
		ExtractionID: extractionID,
		PdfPath:      pdfPath,
		SchemaID:     schemaID,
		DocHash:      docHash,
		RegionsJSON:  regionsJSON,
		SerialNumber: serialNumber,
	})
}

// PDFCorrectionArgs mirrors iqueue.PDFCorrectionArgs.
type PDFCorrectionArgs struct {
	CorrectionID     string
	ExtractionID     string
	PdfPath          string
	FieldName        string
	OriginalValue    string
	CorrectedValue   string
	SerialNumber     int
	OriginalFilename string
}

// SubmitPDFCorrectionTask replaces iqueue.EnqueuePDFCorrectionTask.
func SubmitPDFCorrectionTask(ctx context.Context, args PDFCorrectionArgs) error {
	return Submit(Job{
		TaskType:         "apply_correction_to_pdf",
		CorrectionID:     args.CorrectionID,
		ExtractionID:     args.ExtractionID,
		PdfPath:          args.PdfPath,
		FieldName:        args.FieldName,
		OriginalValue:    args.OriginalValue,
		CorrectedValue:   args.CorrectedValue,
		SerialNumber:     args.SerialNumber,
		OriginalFilename: args.OriginalFilename,
	})
}
