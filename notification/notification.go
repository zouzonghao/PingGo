package notification

import (
	"fmt"
	"log"
	"os"
	"ping-go/config"
	"time"

	"github.com/resend/resend-go/v3"
)

// SendEmail sends an email using Resend with retry logic
func SendEmail(to []string, subject, htmlContent string) error {
	apiKey := config.GlobalConfig.Notification.ResendAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("RESEND_API_KEY")
	}

	if apiKey == "" {
		return fmt.Errorf("RESEND_API_KEY is not set")
	}

	client := resend.NewClient(apiKey)

	fromEmail := config.GlobalConfig.Notification.FromEmail
	if fromEmail == "" {
		fromEmail = "onboarding@resend.dev"
	}
	fromName := config.GlobalConfig.Notification.FromName
	if fromName == "" {
		fromName = "PingGo Monitor"
	}

	params := &resend.SendEmailRequest{
		From:    fmt.Sprintf("%s <%s>", fromName, fromEmail),
		To:      to,
		Subject: subject,
		Html:    htmlContent,
	}

	// Retry logic: 3 attempts with exponential backoff
	var err error
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		_, err = client.Emails.Send(params)
		if err == nil {
			log.Printf("Email sent successfully to %v", to)
			return nil
		}

		log.Printf("Failed to send email (attempt %d/%d): %v", i+1, maxRetries, err)
		if i < maxRetries-1 {
			time.Sleep(time.Duration(2*(i+1)) * time.Second)
		}
	}

	return fmt.Errorf("failed to send email after %d attempts: %w", maxRetries, err)
}
