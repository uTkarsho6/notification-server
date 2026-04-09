package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	firestoreClient  *firestore.Client
	messagingClient  *messaging.Client
	knownDonationIDs = make(map[string]string) // donationID -> status
)

func main() {
	ctx := context.Background()

	log.Println("🔧 Initializing Firebase...")

	// Support two modes:
	// 1. FIREBASE_CREDENTIALS_JSON env var (for Render cloud deployment)
	// 2. File path via GOOGLE_APPLICATION_CREDENTIALS (for local Docker)
	var opt option.ClientOption
	credsJSON := os.Getenv("FIREBASE_CREDENTIALS_JSON")
	if credsJSON != "" {
		log.Println("🔑 Using credentials from FIREBASE_CREDENTIALS_JSON env var")
		opt = option.WithCredentialsJSON([]byte(credsJSON))
	} else {
		credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if credFile == "" {
			credFile = "serviceAccountKey.json"
		}
		log.Printf("🔑 Using credentials file: %s", credFile)
		opt = option.WithCredentialsFile(credFile)
	}

	app, err := firebase.NewApp(ctx, nil, opt)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Firebase app: %v", err)
	}

	// Initialize Firestore client
	firestoreClient, err = app.Firestore(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Firestore: %v", err)
	}
	defer firestoreClient.Close()

	// Initialize FCM Messaging client
	messagingClient, err = app.Messaging(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to initialize FCM: %v", err)
	}

	log.Println("✅ Firebase initialized successfully!")

	// Pre-load existing donation IDs so we don't notify on startup
	preloadExistingDonations(ctx)

	// Start the real-time Firestore listener in a goroutine
	go listenToDonations(ctx)

	// Start the expiry ticker in a goroutine (runs every 30 minutes)
	go startExpiryTicker(ctx)

	// Start a simple health-check HTTP server (required by Render to keep the service alive)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"ok","service":"annapurna-notification-server"}`)
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"status":"healthy"}`)
	})

	log.Printf("🚀 Notification server listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// preloadExistingDonations stores current donation IDs so we skip them during the first snapshot
func preloadExistingDonations(ctx context.Context) {
	docs, err := firestoreClient.Collection("donations").Documents(ctx).GetAll()
	if err != nil {
		log.Printf("⚠️ Failed to preload donations: %v", err)
		return
	}
	for _, doc := range docs {
		statusVal, _ := doc.Data()["status"].(string)
		knownDonationIDs[doc.Ref.ID] = statusVal
	}
	log.Printf("📦 Preloaded %d existing donations", len(knownDonationIDs))
}

// listenToDonations opens a real-time snapshot listener on the donations collection
func listenToDonations(ctx context.Context) {
	snapIter := firestoreClient.Collection("donations").Snapshots(ctx)
	defer snapIter.Stop()

	for {
		snap, err := snapIter.Next()
		if err != nil {
			if status.Code(err) == codes.Canceled {
				log.Println("🛑 Listener stopped")
				return
			}
			log.Printf("⚠️ Snapshot error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, change := range snap.Changes {
			doc := change.Doc
			donationID := doc.Ref.ID
			data := doc.Data()

			foodName, _ := data["foodName"].(string)
			currentStatus, _ := data["status"].(string)
			donorUid, _ := data["donorUid"].(string)

			switch change.Kind {
			case firestore.DocumentAdded:
				// Check if this is a genuinely new donation (not from preload)
				if _, exists := knownDonationIDs[donationID]; !exists {
					log.Printf("🆕 New donation detected: %s (%s)", foodName, donationID)
					knownDonationIDs[donationID] = currentStatus
					go notifyAllReceivers(ctx, foodName)
				}

			case firestore.DocumentModified:
				previousStatus := knownDonationIDs[donationID]
				knownDonationIDs[donationID] = currentStatus

				// Detect status change: available -> claimed
				if previousStatus == "available" && currentStatus == "claimed" {
					log.Printf("🤝 Donation claimed: %s (%s)", foodName, donationID)
					go notifyDonor(ctx, donorUid, foodName)
				}
			}
		}
	}
}

// notifyAllReceivers sends a push notification to all users with role == "Receiver"
func notifyAllReceivers(ctx context.Context, foodName string) {
	docs, err := firestoreClient.Collection("users").Where("role", "==", "Receiver").Documents(ctx).GetAll()
	if err != nil {
		log.Printf("⚠️ Failed to query receivers: %v", err)
		return
	}

	var tokens []string
	for _, doc := range docs {
		token, ok := doc.Data()["fcmToken"].(string)
		if ok && token != "" {
			tokens = append(tokens, token)
		}
	}

	if len(tokens) == 0 {
		log.Println("⚠️ No receiver FCM tokens found")
		return
	}

	// FCM supports max 500 tokens per multicast
	message := &messaging.MulticastMessage{
		Notification: &messaging.Notification{
			Title: "🍲 New Food Available!",
			Body:  fmt.Sprintf("A donor just posted: %s. Claim it before it's gone!", foodName),
		},
		Tokens: tokens,
	}

	response, err := messagingClient.SendEachForMulticast(ctx, message)
	if err != nil {
		log.Printf("⚠️ FCM multicast error: %v", err)
		return
	}

	log.Printf("📨 Notified %d/%d receivers about new donation: %s", response.SuccessCount, len(tokens), foodName)
}

// notifyDonor sends a push notification to a specific donor when their food is claimed
func notifyDonor(ctx context.Context, donorUid string, foodName string) {
	if donorUid == "" {
		log.Println("⚠️ Empty donorUid, skipping notification")
		return
	}

	doc, err := firestoreClient.Collection("users").Doc(donorUid).Get(ctx)
	if err != nil {
		log.Printf("⚠️ Failed to fetch donor %s: %v", donorUid, err)
		return
	}

	token, ok := doc.Data()["fcmToken"].(string)
	if !ok || token == "" {
		log.Printf("⚠️ Donor %s has no FCM token", donorUid)
		return
	}

	message := &messaging.Message{
		Notification: &messaging.Notification{
			Title: "✅ Your Food Was Claimed!",
			Body:  fmt.Sprintf("Great news! Your donation '%s' has been claimed by an NGO.", foodName),
		},
		Token: token,
	}

	result, err := messagingClient.Send(ctx, message)
	if err != nil {
		log.Printf("⚠️ Failed to notify donor %s: %v", donorUid, err)
		return
	}

	log.Printf("📨 Notified donor %s: %s (messageID: %s)", donorUid, foodName, result)
}

// startExpiryTicker runs every 30 minutes and marks expired donations
func startExpiryTicker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// Run once immediately on startup
	expireDonations(ctx)

	for {
		select {
		case <-ticker.C:
			expireDonations(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// expireDonations queries Firestore for overdue donations and marks them expired
func expireDonations(ctx context.Context) {
	now := time.Now()
	docs, err := firestoreClient.Collection("donations").
		Where("status", "==", "available").
		Documents(ctx).GetAll()
	if err != nil {
		log.Printf("⚠️ Failed to query for expiry: %v", err)
		return
	}

	expiredCount := 0
	for _, doc := range docs {
		data := doc.Data()
		pickupTime, ok := data["pickupTime"].(time.Time)
		if !ok {
			continue
		}
		if pickupTime.Before(now) {
			_, err := doc.Ref.Update(ctx, []firestore.Update{
				{Path: "status", Value: "expired"},
			})
			if err != nil {
				log.Printf("⚠️ Failed to expire donation %s: %v", doc.Ref.ID, err)
			} else {
				expiredCount++
				knownDonationIDs[doc.Ref.ID] = "expired"
			}
		}
	}

	if expiredCount > 0 {
		log.Printf("🕐 Auto-expired %d donations", expiredCount)
	}
}
