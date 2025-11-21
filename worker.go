package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"
	"github.com/streadway/amqp"
)

const (
	receiveMessageQueueName = "q.message.receive"
	sendMessageQueueName    = "q.message.send"
	// allowedPhoneNumber      = "554288872501" // Uncomment for local environment filtering
)

var (
	amqpHost     = os.Getenv("AMQP_HOST")
	amqpPort     = os.Getenv("AMQP_PORT")
	amqpUser     = os.Getenv("AMQP_USER")
	amqpPassword = os.Getenv("AMQP_PASSWORD")

	rabbitMQURL     = "amqp://" + amqpUser + ":" + amqpPassword + "@" + amqpHost + ":" + amqpPort + "/"
	rabbitMQChannel *amqp.Channel
	rabbitMQConn    *amqp.Connection
)

func failOnErrorWithTransaction(err error, transactionId string) {
	if err != nil {
		logWithTransaction(transactionId, "ERROR", err.Error())
		panic(err)
	}
}

func logWithTransaction(transactionId string, level string, message string) {
	log.Println(level + ": " + transactionId + " - " + message)
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
	}
}

type SendMessagePayload struct {
	MessageType     string `json:"message_type"`
	QuotedMessageID string `json:"quoted_message_id"`
	RecipientNumber string `json:"recipient_number"`
	MessageBody     string `json:"message_body"`
	TransactionId   string `json:"transaction_id"`
}

type ReceiveMessagePayload struct {
	MessageType     string `json:"message_type"`
	MessageId       string `json:"message_id"`
	SenderNumber    string `json:"sender_number"`
	MessageBody     string `json:"message_body"`
	TransactionId   string `json:"transaction_id"`
	QuotedMessageID string `json:"quoted_message_id"`
}

func NewReceiveMessagePayload() *ReceiveMessagePayload {
	return &ReceiveMessagePayload{
		TransactionId: uuid.New().String(),
	}
}

func NewSendMessagePayload() *SendMessagePayload {
	return &SendMessagePayload{
		TransactionId: uuid.New().String(),
	}
}

func setUpRabbitMQConsumer(channel *amqp.Channel) <-chan amqp.Delivery {
	messages, err := channel.Consume(
		sendMessageQueueName,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Error starting consumer")

	return messages
}

func handleSendWhatsappMessage(client *whatsmeow.Client, body []byte) {
	payload := NewSendMessagePayload()
	err := json.Unmarshal(body, &payload)
	failOnErrorWithTransaction(err, payload.TransactionId)

	recipientJID := types.NewJID(payload.RecipientNumber, types.DefaultUserServer)

	recipientJIDString := recipientJID.String()

	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(payload.MessageBody),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:    proto.String(payload.QuotedMessageID),
				Participant: &recipientJIDString,
			},
		},
	}

	logWithTransaction(payload.TransactionId, "INFO", "Sending message")

	_, err = client.SendMessage(context.Background(), recipientJID, msg)

	failOnErrorWithTransaction(err, payload.TransactionId)
}

func initRabbitMQ() *amqp.Channel {
	connection, err := amqp.Dial(rabbitMQURL)
	failOnError(err, "Could not connect to RabbitMQ host")

	channel, err := connection.Channel()
	failOnError(err, "Could not create RabbitMQ channel")

	// Store connection for reconnection
	rabbitMQConn = connection

	// Create queues if they don't exist
	createQueues(channel)

	return channel
}

func ensureRabbitMQConnection() {
	// Always try to reconnect if channel is nil or if we get an error
	if rabbitMQChannel == nil {
		log.Println("RabbitMQ channel is nil, reconnecting...")
		
		// Close existing connection if any
		if rabbitMQConn != nil {
			rabbitMQConn.Close()
		}
		
		// Reconnect
		rabbitMQChannel = initRabbitMQ()
		log.Println("RabbitMQ reconnected successfully")
	}
}

func publishWithRetry(transactionId string, msgData []byte) error {
	maxRetries := 3
	
	for i := 0; i < maxRetries; i++ {
		// Force reconnect on every attempt
		rabbitMQChannel = nil
		rabbitMQConn = nil
		ensureRabbitMQConnection()
		
		err := rabbitMQChannel.Publish(
			"",
			receiveMessageQueueName,
			false,
			false,
			amqp.Publishing{
				ContentType: "application/json",
				Body:        msgData,
			},
		)
		
		if err == nil {
			return nil // Success
		}
		
		logWithTransaction(transactionId, "WARN", fmt.Sprintf("Publish attempt %d failed: %s", i+1, err.Error()))
		
		// Wait a bit before retry
		if i < maxRetries-1 {
			time.Sleep(time.Second * 2)
		}
	}
	
	return fmt.Errorf("failed to publish after %d attempts", maxRetries)
}

// extractPhoneNumberFromJID extrai o número de telefone do JID
func extractPhoneNumberFromJID(jid types.JID) string {
	if jid.User == "" {
		return ""
	}
	return jid.User
}

// getRealPhoneNumber detecta qual JID contém o número real do WhatsApp
// O número real sempre tem servidor @s.whatsapp.net, enquanto LID tem @lid
func getRealPhoneNumber(sender types.JID, senderAlt types.JID) string {
	// Verifica se Sender é o número real (termina com @s.whatsapp.net)
	if sender.Server == types.DefaultUserServer {
		return extractPhoneNumberFromJID(sender)
	}
	
	// Se Sender não é o número real, verifica SenderAlt
	if senderAlt.Server == types.DefaultUserServer {
		return extractPhoneNumberFromJID(senderAlt)
	}
	
	// Fallback: se nenhum tiver o servidor correto, usa Sender
	if sender.User != "" {
		return sender.User
	}
	
	// Último fallback: usa SenderAlt
	return extractPhoneNumberFromJID(senderAlt)
}

func createQueues(channel *amqp.Channel) {
	// Create exchange for send messages
	err := channel.ExchangeDeclare(
		"whatsapp.send",  // exchange name
		"topic",          // type
		true,             // durable
		false,            // auto-delete
		false,            // internal
		false,            // no-wait
		nil,              // arguments
	)
	if err != nil {
		log.Printf("Error creating send exchange: %v", err)
	} else {
		log.Println("Send exchange created successfully")
	}

	// Create send message queue
	_, err = channel.QueueDeclare(
		sendMessageQueueName, // name
		true,                  // durable
		false,                 // delete when unused
		false,                 // exclusive
		false,                 // no-wait
		nil,                   // arguments
	)
	if err != nil {
		log.Printf("Error creating send queue: %v", err)
	} else {
		log.Println("Send queue created successfully")
	}

	// Bind send queue to exchange
	err = channel.QueueBind(
		sendMessageQueueName, // queue name
		"send",               // routing key
		"whatsapp.send",      // exchange name
		false,                // no-wait
		nil,                  // arguments
	)
	if err != nil {
		log.Printf("Error binding send queue: %v", err)
	} else {
		log.Println("Send queue bound to exchange successfully")
	}

	// Create receive message queue
	_, err = channel.QueueDeclare(
		receiveMessageQueueName, // name
		true,                    // durable
		false,                   // delete when unused
		false,                   // exclusive
		false,                   // no-wait
		nil,                     // arguments
	)
	if err != nil {
		log.Printf("Error creating receive queue: %v", err)
	} else {
		log.Println("Receive queue created successfully")
	}
}

func messageReceiveHandler(client *whatsmeow.Client) func(interface{}) {
	return func(evt interface{}) {
		// Debug: mostrar o objeto completo recebido
		evtJSON, err := json.MarshalIndent(evt, "", "  ")
		if err != nil {
			log.Printf("DEBUG: Erro ao serializar evento: %v", err)
			log.Printf("DEBUG: Tipo do evento: %T", evt)
			log.Printf("DEBUG: Evento (formato string): %+v", evt)
		} else {
			log.Printf("DEBUG: Objeto completo recebido:\n%s", string(evtJSON))
		}
		
	switch evt := evt.(type) {
	case *events.Message:
		// Extrair o número de telefone correto detectando qual JID contém o número real
		// Quando usuário não está cadastrado: Sender é LID (@lid) e SenderAlt é número real (@s.whatsapp.net)
		// Quando usuário está cadastrado: Sender é número real (@s.whatsapp.net) e SenderAlt é LID (@lid)
		senderNumber := getRealPhoneNumber(evt.Info.Sender, evt.Info.SenderAlt)
		
		// Debug log for all messages
		log.Printf("DEBUG: Received message - Type: %s, From: %s (Sender: %s [%s], SenderAlt: %s [%s]), IsFromMe: %v, IsGroup: %v", 
			evt.Info.Type, senderNumber, evt.Info.Sender.User, evt.Info.Sender.Server, 
			evt.Info.SenderAlt.User, evt.Info.SenderAlt.Server, evt.Info.IsFromMe, evt.Info.IsGroup)
		
		// Ignore messages from bot itself or groups
		if evt.Info.IsFromMe || evt.Info.IsGroup {
			log.Printf("DEBUG: Ignoring message - IsFromMe: %v, IsGroup: %v", evt.Info.IsFromMe, evt.Info.IsGroup)
			return
		}

		// Filter by allowed phone number (commented for production)
		// if senderNumber != allowedPhoneNumber {
		// 	log.Printf("DEBUG: Ignoring message from unauthorized number: %s (allowed: %s)", senderNumber, allowedPhoneNumber)
		// 	return
		// }

		// Check if message type is not text
		if evt.Info.Type != "text" {
			log.Printf("DEBUG: Non-text message detected - Type: %s, From: %s", evt.Info.Type, senderNumber)
			// Send automatic response for non-text messages
			sendUnsupportedMessageTypeResponse(client, senderNumber, evt.Info.Type)
			return
		}

		payload := NewReceiveMessagePayload()
		payload.MessageType = "text"
		payload.SenderNumber = senderNumber
		payload.MessageId = evt.Info.ID

		if evt.Message.Conversation != nil {
			payload.MessageBody = *evt.Message.Conversation
		} else if evt.Message.ExtendedTextMessage != nil {
			payload.MessageBody = evt.Message.ExtendedTextMessage.GetText()
			if evt.Message.ExtendedTextMessage.ContextInfo != nil {
				if evt.Message.ExtendedTextMessage.ContextInfo.StanzaID != nil {
					payload.QuotedMessageID = *evt.Message.ExtendedTextMessage.ContextInfo.StanzaID
				}
			}
		}

		logWithTransaction(payload.TransactionId, "INFO", "Received message from "+payload.SenderNumber)

		msgData, err := json.Marshal(payload)
		if err != nil {
			logWithTransaction(payload.TransactionId, "ERROR", "Failed to marshal message: "+err.Error())
			return
		}

		// Always reconnect before publishing
		rabbitMQChannel = nil
		rabbitMQConn = nil
		ensureRabbitMQConnection()

		err = rabbitMQChannel.Publish(
			"",
			receiveMessageQueueName,
			false,
			false,
			amqp.Publishing{
				ContentType: "application/json",
				Body:        msgData,
			},
		)
		if err != nil {
			logWithTransaction(payload.TransactionId, "ERROR", "Failed to publish message: "+err.Error())
			return
		}
		
		logWithTransaction(payload.TransactionId, "INFO", "Message published to RabbitMQ successfully")
		}
	}
}

func sendUnsupportedMessageTypeResponse(client *whatsmeow.Client, senderNumber string, messageType string) {
	log.Printf("Received unsupported message type '%s' from %s, sending auto-response", messageType, senderNumber)
	
	// Create auto-response message
	responsePayload := NewSendMessagePayload()
	responsePayload.MessageType = "text"
	responsePayload.RecipientNumber = senderNumber
	responsePayload.MessageBody = "Olá! No momento, só consigo processar mensagens de texto. Por favor, envie sua mensagem em formato de texto."
	
	logWithTransaction(responsePayload.TransactionId, "INFO", "Sending auto-response for unsupported message type")
	
	// Send the response message directly
	msgData, _ := json.Marshal(responsePayload)
	handleSendWhatsappMessage(client, msgData)
}

func main() {
	dbLog := waLog.Stdout("Database", "DEBUG", true)

	container, err := sqlstore.New(context.Background(), "sqlite3", "file:storage/wadb.db?_foreign_keys=on", dbLog)
	failOnError(err, "Error creating db container")

	deviceStore, err := container.GetFirstDevice(context.Background())
	failOnError(err, "Error getting device")

	clientLog := waLog.Stdout("Client", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AutomaticMessageRerequestFromPhone = true
	client.AddEventHandler(messageReceiveHandler(client))

	rabbitMQChannel = initRabbitMQ()

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		failOnError(err, "Error connecting to client")

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			}
		}
	} else {
		err = client.Connect()
		failOnError(err, "Error connecting to client")
	}

	log.Println("Setting up RabbitMQ consumer for q.message.send...")
	messages := setUpRabbitMQConsumer(rabbitMQChannel)
	log.Println("RabbitMQ consumer setup completed successfully")

	go func() {
		log.Println("Starting consumer goroutine...")
		for message := range messages {
			log.Printf("Received message from q.message.send: %s", string(message.Body))
			handleSendWhatsappMessage(client, message.Body)
			message.Ack(false)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
