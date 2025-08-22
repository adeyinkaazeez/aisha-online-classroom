package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strconv"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Client represents a single WebSocket connection in a room
type Client struct {
	conn     *websocket.Conn
	roomID   string
	userID   string
	username string // Store username in client struct
}

// FileInfo represents a shared file
type FileInfo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	Type       string    `json:"type"`
	UploadedBy string    `json:"uploadedBy"`
	UploadedAt time.Time `json:"uploadedAt"`
	Path       string    `json:"path"`
	IsImage    bool      `json:"isImage"`
	IsPDF      bool      `json:"isPDF"`
	IsDocument bool      `json:"isDocument"`
}

// RecordingSession represents a recording session for a room
type RecordingSession struct {
	ID              string    `json:"id"`
	RoomID          string    `json:"roomId"`
	StartTime       time.Time `json:"startTime"`
	EndTime         time.Time `json:"endTime,omitempty"`
	TeacherID       string    `json:"teacherId"`
	TeacherUsername string    `json:"teacherUsername"`
	Subject         string    `json:"subject"`
	Status          string    `json:"status"` // "active", "completed", "failed"
	WhiteboardData  []Message `json:"whiteboardData"`
	VideoURL        string    `json:"videoUrl,omitempty"`
	AudioURL        string    `json:"audioUrl,omitempty"`
	ScreenShareURL  string    `json:"screenShareUrl,omitempty"`
	Duration        int64     `json:"duration,omitempty"` // in seconds
}

// Quiz structures
type Question struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Correct int      `json:"correct"` // Index of correct answer
	Points  int      `json:"points"`
}

type Quiz struct {
	ID          string     `json:"id"`
	RoomID      string     `json:"roomId"`
	Title       string     `json:"title"`
	Questions   []Question `json:"questions"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	Status      string     `json:"status"`    // "active", "closed"
	TimeLimit   int        `json:"timeLimit"` // in minutes
	TotalPoints int        `json:"totalPoints"`
}

type QuizAnswer struct {
	QuestionID string `json:"questionId"`
	Answer     int    `json:"answer"` // Selected option index
}

type QuizSubmission struct {
	ID          string       `json:"id"`
	QuizID      string       `json:"quizId"`
	StudentID   string       `json:"studentId"`
	StudentName string       `json:"studentName"`
	Answers     []QuizAnswer `json:"answers"`
	SubmittedAt time.Time    `json:"submittedAt"`
	Score       int          `json:"score"`
	MaxScore    int          `json:"maxScore"`
	Graded      bool         `json:"graded"`
	Feedback    string       `json:"feedback,omitempty"`
}

// Room represents a classroom
type Room struct {
	clients                  map[string]*Client // userID -> Client
	mutex                    sync.RWMutex       // Protect concurrent access to clients map
	TeacherID                string             // Stores the userID of the teacher for this room
	GrantedWhiteboardAccess  map[string]bool    // userID -> true if granted
	GrantedScreenShareAccess map[string]bool    // userID -> true if granted screen sharing permission
	DrawingHistory           []Message          // Stores the history of 'draw', 'text', and 'shape' messages for persistence
	RedoStack                []Message          // Stores undone entries to support redo
	Subject                  string             // Class subject name set by the teacher
	SharedFiles              []FileInfo         // Files shared in this room
	RecordingActive          bool               // Indicates if recording is active for this room
	CurrentRecording         *RecordingSession  // Current recording session
	RecordingMutex           sync.Mutex         // Protect recording operations
	RaisedHands              map[string]bool    // userID -> true if hand is raised
	Quizzes                  []Quiz             // Quizzes created for this room
	QuizSubmissions          []QuizSubmission   // Quiz submissions from students
	QuizMutex                sync.Mutex         // Protect quiz operations
}

var rooms = make(map[string]*Room)
var roomsMutex sync.RWMutex // Protect concurrent access to rooms map

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// RosterEntry represents a single user in the class roster
type RosterEntry struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
}

// Message defines the structure for WebSocket messages exchanged between client and server.
type Message struct {
	Type     string      `json:"type"`
	RoomID   string      `json:"roomId,omitempty"`
	UserID   string      `json:"userId,omitempty"`
	Username string      `json:"username,omitempty"`
	Payload  interface{} `json:"payload,omitempty"`

	SDP       interface{} `json:"sdp,omitempty"`
	Candidate interface{} `json:"candidate,omitempty"`
	To        string      `json:"to,omitempty"`
	From      string      `json:"from,omitempty"`

	// Drawing specific fields
	X1    float64 `json:"x1,omitempty"`
	Y1    float64 `json:"y1,omitempty"`
	X2    float64 `json:"x2,omitempty"`
	Y2    float64 `json:"y2,omitempty"`
	Color string  `json:"color,omitempty"`
	// Optional line width for strokes (e.g., eraser)
	LineWidth float64 `json:"lineWidth,omitempty"`

	// Text specific fields
	TextContent    string  `json:"textContent,omitempty"`
	TextX          float64 `json:"textX,omitempty"`
	TextY          float64 `json:"textY,omitempty"`
	FontSize       string  `json:"fontSize,omitempty"`
	FontFamily     string  `json:"fontFamily,omitempty"`
	FontWeight     string  `json:"fontWeight,omitempty"`
	FontStyle      string  `json:"fontStyle,omitempty"`
	TextDecoration string  `json:"textDecoration,omitempty"`

	// Shape specific fields (NEW)
	Shape       string  `json:"shape"`
	ShapeType   string  `json:"shapeType,omitempty"`
	ShapeX      float64 `json:"shapeX,omitempty"`
	ShapeY      float64 `json:"shapeY,omitempty"`
	ShapeWidth  float64 `json:"shapeWidth,omitempty"`
	ShapeHeight float64 `json:"shapeHeight,omitempty"`
	ShapeRadius float64 `json:"shapeRadius,omitempty"`
	ShapeColor  string  `json:"shapeColor,omitempty"`

	// Graph function specific fields
	Expression string      `json:"expression,omitempty"`
	Points     interface{} `json:"points,omitempty"`

	TeacherID                string `json:"teacherId,omitempty"`
	TargetUserID             string `json:"targetUserId,omitempty"`
	TargetUsername           string `json:"targetUsername,omitempty"`
	HasPermission            bool   `json:"hasPermission,omitempty"`
	HasScreenSharePermission bool   `json:"hasScreenSharePermission,omitempty"`

	// File sharing fields
	FileID         string     `json:"fileId,omitempty"`
	FileName       string     `json:"fileName,omitempty"`
	FileSize       int64      `json:"fileSize,omitempty"`
	FileType       string     `json:"fileType,omitempty"`
	FileUploadedBy string     `json:"fileUploadedBy,omitempty"`
	FileUploadedAt string     `json:"fileUploadedAt,omitempty"`
	SharedFiles    []FileInfo `json:"sharedFiles,omitempty"`

	// Recording specific fields
	RecordingID    string             `json:"recordingId,omitempty"`
	RecordingData  interface{}        `json:"recordingData,omitempty"`
	RecordingType  string             `json:"recordingType,omitempty"` // "whiteboard", "video", "audio", "screen"
	Timestamp      int64              `json:"timestamp,omitempty"`
	RecordingState string             `json:"recordingState,omitempty"` // "start", "stop", "pause", "resume"
	Recordings     []RecordingSession `json:"recordings,omitempty"`

	// Quiz specific fields
	QuizID         string           `json:"quizId,omitempty"`
	QuizData       Quiz             `json:"quizData,omitempty"`
	QuizAnswers    []QuizAnswer     `json:"quizAnswers,omitempty"`
	QuizSubmission QuizSubmission   `json:"quizSubmission,omitempty"`
	Quizzes        []Quiz           `json:"quizzes,omitempty"`
	Submissions    []QuizSubmission `json:"submissions,omitempty"`

	WhiteboardHistory []Message     `json:"whiteboardHistory,omitempty"`
	CurrentRoster     []RosterEntry `json:"currentRoster,omitempty"`
	Subject           string        `json:"subject,omitempty"`
	IsTyping          bool          `json:"isTyping,omitempty"`
}

// generateUniqueUserID generates a unique ID for each client using the google/uuid library.
func generateUniqueUserID() string {
	return uuid.New().String()
}

// calculateTotalPoints calculates the total points for a quiz
func calculateTotalPoints(questions []Question) int {
	total := 0
	for _, q := range questions {
		total += q.Points
	}
	return total
}

// calculateScore calculates the score for a quiz submission
func calculateScore(questions []Question, answers []QuizAnswer) int {
	score := 0
	answerMap := make(map[string]int)
	for _, answer := range answers {
		answerMap[answer.QuestionID] = answer.Answer
	}

	for _, question := range questions {
		if answer, exists := answerMap[question.ID]; exists {
			if answer == question.Correct {
				score += question.Points
			}
		}
	}
	return score
}

// getRoomRoster generates the current roster for a given room.
func getRoomRoster(room *Room) []RosterEntry {
	roster := make([]RosterEntry, 0, len(room.clients))
	room.mutex.RLock()
	for _, client := range room.clients {
		roster = append(roster, RosterEntry{UserID: client.userID, Username: client.username})
	}
	room.mutex.RUnlock()
	return roster
}

// saveQuizData saves quiz data to a JSON file
func saveQuizData(roomID string, quizzes []Quiz, submissions []QuizSubmission) {
	quizDir := filepath.Join("backend/quizzes", roomID)
	if err := os.MkdirAll(quizDir, 0755); err != nil {
		log.Printf("Error creating quiz directory: %v", err)
		return
	}

	// Save quizzes
	quizzesFile := filepath.Join(quizDir, "quizzes.json")
	file, err := os.Create(quizzesFile)
	if err != nil {
		log.Printf("Error creating quizzes file: %v", err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(quizzes); err != nil {
		log.Printf("Error encoding quizzes: %v", err)
		return
	}

	// Save submissions
	submissionsFile := filepath.Join(quizDir, "submissions.json")
	file, err = os.Create(submissionsFile)
	if err != nil {
		log.Printf("Error creating submissions file: %v", err)
		return
	}
	defer file.Close()

	encoder = json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(submissions); err != nil {
		log.Printf("Error encoding submissions: %v", err)
		return
	}

	log.Printf("Successfully saved quiz data for room %s", roomID)
}

// wsHandler handles WebSocket connections, managing clients and message routing.
func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	roomID := r.URL.Query().Get("room")
	username := r.URL.Query().Get("username")
	if roomID == "" || username == "" {
		log.Println("Missing room ID or username in WebSocket URL")
		conn.WriteJSON(Message{Type: "error", Payload: "Missing room ID or username"})
		return
	}

	userID := generateUniqueUserID()
	client := &Client{conn: conn, roomID: roomID, userID: userID, username: username}

	roomsMutex.Lock()
	room, ok := rooms[roomID]
	if !ok {
		room = &Room{
			clients:                  make(map[string]*Client),
			TeacherID:                userID,
			GrantedWhiteboardAccess:  make(map[string]bool),
			GrantedScreenShareAccess: make(map[string]bool),
			DrawingHistory:           make([]Message, 0),
			RaisedHands:              make(map[string]bool),
			Quizzes:                  make([]Quiz, 0),
			QuizSubmissions:          make([]QuizSubmission, 0),
		}
		rooms[roomID] = room
		log.Printf("New room %s created by %s (Teacher ID: %s)\n", roomID, username, userID)
	}
	roomsMutex.Unlock()

	room.mutex.Lock()
	room.clients[userID] = client
	room.mutex.Unlock()

	log.Printf("Client %s (%s) joined room %s\n", username, userID, roomID)

	initialMessage := Message{
		Type:                     "assigned-id",
		UserID:                   userID,
		TeacherID:                room.TeacherID,
		HasPermission:            (userID == room.TeacherID || room.GrantedWhiteboardAccess[userID]),
		HasScreenSharePermission: (userID == room.TeacherID || room.GrantedScreenShareAccess[userID]),
		Subject:                  room.Subject,
	}
	log.Printf("Sending assigned-id message to user %s in room %s with subject: %s\n", username, roomID, room.Subject)

	room.mutex.RLock()
	initialMessage.WhiteboardHistory = make([]Message, len(room.DrawingHistory))
	copy(initialMessage.WhiteboardHistory, room.DrawingHistory)
	initialMessage.SharedFiles = make([]FileInfo, len(room.SharedFiles))
	copy(initialMessage.SharedFiles, room.SharedFiles)
	room.mutex.RUnlock()

	initialMessage.CurrentRoster = getRoomRoster(room)

	// Send active quizzes to students
	room.QuizMutex.Lock()
	var activeQuizzes []Quiz
	for _, quiz := range room.Quizzes {
		if quiz.Status == "active" {
			activeQuizzes = append(activeQuizzes, quiz)
		}
	}
	initialMessage.Quizzes = activeQuizzes
	room.QuizMutex.Unlock()

	err = client.conn.WriteJSON(initialMessage)
	if err != nil {
		log.Println("Error sending assigned-id/initial state to client:", err)
		return
	}

	// Send active quizzes individually to students (not teachers)
	if userID != room.TeacherID {
		room.QuizMutex.Lock()
		for _, quiz := range room.Quizzes {
			if quiz.Status == "active" {
				client.conn.WriteJSON(Message{
					Type:     "quiz-created",
					QuizData: quiz,
				})
			}
		}
		room.QuizMutex.Unlock()
	}

	userJoinedMsg := Message{
		Type:          "user-joined",
		UserID:        userID,
		Username:      username,
		TeacherID:     room.TeacherID,
		CurrentRoster: getRoomRoster(room),
		Subject:       room.Subject,
	}
	log.Printf("Broadcasting user-joined message to room %s for user %s with subject: %s\n", roomID, username, room.Subject)
	broadcastToRoom(roomID, userJoinedMsg, userID)

	if userID == room.TeacherID {
		room.mutex.RLock()
		for grantedUserID := range room.GrantedWhiteboardAccess {
			grantedUsername := ""
			if c, ok := room.clients[grantedUserID]; ok {
				grantedUsername = c.username
			} else {
				grantedUsername = grantedUserID
			}

			err := client.conn.WriteJSON(Message{
				Type:           "whiteboard-permission-changed",
				TargetUserID:   grantedUserID,
				TargetUsername: grantedUsername,
				HasPermission:  true,
			})
			if err != nil {
				log.Println("Error sending initial granted permission to teacher:", err)
			}
		}
		room.mutex.RUnlock()
	}

	for {
		var msg Message
		_, messageBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Client %s (%s) disconnected from room %s\n", username, userID, roomID)
			} else {
				log.Println("Read error:", err)
			}
			break
		}

		// Log the raw message bytes for debugging
		log.Printf("Received raw message from %s (%s): %s\n", username, userID, string(messageBytes))

		// Add additional debugging for screen share permission responses
		if strings.Contains(string(messageBytes), "screen_share_permission_response") {
			log.Printf("DEBUG: Raw screen share permission response message: %s", string(messageBytes))
		}

		err = json.Unmarshal(messageBytes, &msg)
		if err != nil {
			log.Println("Error unmarshaling JSON:", err)
			continue
		}

		switch msg.Type {
		case "offer", "answer", "candidate":
			if msg.To != "" {
				msg.From = userID
				relayToClient(roomID, msg.To, msg)
			} else {
				log.Printf("WebRTC message (%s) received without a 'To' recipient from %s\n", msg.Type, userID)
			}
		case "draw", "text", "shape", "graph-function": // Now handling 'draw', 'text', 'shape', and 'graph-function' messages
			// Normalize shape field for compatibility
			if msg.Type == "shape" {
				log.Printf("DEBUG: Received shape message - Shape: '%s', ShapeType: '%s'", msg.Shape, msg.ShapeType)
				if msg.Shape == "" && msg.ShapeType != "" {
					msg.Shape = msg.ShapeType
				}
				// Ensure shape is set from the raw JSON
				if msg.Shape == "" {
					// Try to extract shape from the raw message
					var rawMsg map[string]interface{}
					if err := json.Unmarshal(messageBytes, &rawMsg); err == nil {
						if shapeVal, ok := rawMsg["shape"].(string); ok && shapeVal != "" {
							msg.Shape = shapeVal
							log.Printf("DEBUG: Extracted shape from raw message: '%s'", msg.Shape)
						}
					}
				}
			}

			// Add specific debugging for graph-function messages
			if msg.Type == "graph-function" {
				log.Printf("DEBUG: Received graph-function message from %s: expression='%s', color='%s', points=%d",
					userID, msg.Expression, msg.Color, len(msg.Points.([]interface{})))
			}

			room.mutex.RLock()
			canDraw := (userID == room.TeacherID || room.GrantedWhiteboardAccess[userID])
			log.Printf("DEBUG: User %s (TeacherID: %s) canDraw: %v, isTeacher: %v, hasGrantedAccess: %v",
				userID, room.TeacherID, canDraw, userID == room.TeacherID, room.GrantedWhiteboardAccess[userID])
			room.mutex.RUnlock()

			if canDraw {
				room.mutex.Lock()
				// Clear redo stack on any new drawing action
				room.RedoStack = nil
				room.DrawingHistory = append(room.DrawingHistory, msg) // Store all whiteboard messages
				room.mutex.Unlock()

				// Add to recording if active
				if room.RecordingActive && room.CurrentRecording != nil {
					room.RecordingMutex.Lock()
					// Add timestamp to the message for recording
					recordingMsg := msg
					recordingMsg.Timestamp = time.Now().Unix()
					room.CurrentRecording.WhiteboardData = append(room.CurrentRecording.WhiteboardData, recordingMsg)
					room.RecordingMutex.Unlock()
				}

				log.Printf("Broadcasting %s message from %s: %+v\n", msg.Type, userID, msg)
				broadcastToRoom(roomID, msg, userID)
			} else {
				log.Printf("Whiteboard action blocked for student %s in room %s: No permission.\n", userID, roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "You do not have permission to use the whiteboard."})
			}
		case "undo":
			// Only teacher can undo
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can undo."})
				break
			}
			room.mutex.Lock()
			if len(room.DrawingHistory) > 0 {
				last := room.DrawingHistory[len(room.DrawingHistory)-1]
				room.DrawingHistory = room.DrawingHistory[:len(room.DrawingHistory)-1]
				room.RedoStack = append(room.RedoStack, last)
			}
			// Snapshot broadcast
			snapshot := make([]Message, len(room.DrawingHistory))
			copy(snapshot, room.DrawingHistory)
			room.mutex.Unlock()
			broadcastToRoom(roomID, Message{Type: "whiteboard-history", WhiteboardHistory: snapshot}, "")
		case "redo":
			// Only teacher can redo
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can redo."})
				break
			}
			room.mutex.Lock()
			if len(room.RedoStack) > 0 {
				last := room.RedoStack[len(room.RedoStack)-1]
				room.RedoStack = room.RedoStack[:len(room.RedoStack)-1]
				room.DrawingHistory = append(room.DrawingHistory, last)
			}
			// Snapshot broadcast
			snapshot := make([]Message, len(room.DrawingHistory))
			copy(snapshot, room.DrawingHistory)
			room.mutex.Unlock()
			broadcastToRoom(roomID, Message{Type: "whiteboard-history", WhiteboardHistory: snapshot}, "")
		case "clear-whiteboard":
			if userID == room.TeacherID {
				room.mutex.Lock()
				room.DrawingHistory = []Message{}
				room.RedoStack = nil
				room.mutex.Unlock()
				broadcastToRoom(roomID, msg, userID)
			} else {
				log.Printf("Clear whiteboard blocked for student %s in room %s: Only teacher can clear.\n", userID, roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can clear the whiteboard."})
			}
		case "clear-graph":
			if userID == room.TeacherID {
				// Remove all graph-function messages from history
				room.mutex.Lock()
				var newHistory []Message
				for _, item := range room.DrawingHistory {
					if item.Type != "graph-function" {
						newHistory = append(newHistory, item)
					}
				}
				room.DrawingHistory = newHistory
				room.RedoStack = nil
				room.mutex.Unlock()
				broadcastToRoom(roomID, msg, userID)
			} else {
				log.Printf("Clear graph blocked for student %s in room %s: Only teacher can clear.\n", userID, roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can clear the graph."})
			}
		case "typing-status":
			// Broadcast typing status to all users in the room
			log.Printf("Received typing-status message from user %s (%s) in room %s: isTyping=%v\n", username, userID, roomID, msg.IsTyping)
			broadcastToRoom(roomID, Message{
				Type:     "typing-status",
				UserID:   userID,
				Username: username,
				IsTyping: msg.IsTyping,
			}, userID)
			log.Printf("Broadcasted typing-status message to room %s\n", roomID)
		case "request-whiteboard-access":
			if userID != room.TeacherID {
				teacherClient, found := func() (*Client, bool) {
					room.mutex.RLock()
					defer room.mutex.RUnlock()
					return room.clients[room.TeacherID], room.TeacherID != ""
				}()

				if found && teacherClient != nil {
					log.Printf("Student %s (%s) requested whiteboard access in room %s. Relaying to teacher %s.\n", username, userID, roomID, room.TeacherID)
					err := teacherClient.conn.WriteJSON(Message{
						Type:           "request-whiteboard-access",
						TargetUserID:   userID,
						TargetUsername: username,
					})
					if err != nil {
						log.Println("Error relaying whiteboard request to teacher:", err)
					}
				} else {
					log.Printf("Teacher not found in room %s to relay whiteboard request from %s\n", roomID, userID)
					client.conn.WriteJSON(Message{Type: "error", Payload: "Teacher not found to process your request."})
				}
			} else {
				log.Printf("Teacher %s tried to request whiteboard access (self-request). Ignoring.\n", userID)
			}
		case "grant-whiteboard-access", "deny-whiteboard-access":
			if room.TeacherID == "" {
				log.Printf("Whiteboard permission change request received but no teacher in room %s. Ignoring.\n", roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "No teacher available to process whiteboard permission change."})
			} else if userID == room.TeacherID && msg.TargetUserID != "" {
				room.mutex.Lock()
				var hasPermission bool
				if msg.Type == "grant-whiteboard-access" {
					room.GrantedWhiteboardAccess[msg.TargetUserID] = true
					hasPermission = true
					log.Printf("Teacher %s granted whiteboard access to student %s in room %s.\n", username, msg.TargetUserID, roomID)
				} else {
					delete(room.GrantedWhiteboardAccess, msg.TargetUserID)
					hasPermission = false
					log.Printf("Teacher %s denied/revoked whiteboard access for student %s in room %s.\n", username, msg.TargetUserID, roomID)
				}
				room.mutex.Unlock()

				broadcastToRoom(roomID, Message{
					Type:           "whiteboard-permission-changed",
					TargetUserID:   msg.TargetUserID,
					TargetUsername: msg.TargetUsername,
					HasPermission:  hasPermission,
				}, "")
			} else {
				log.Printf("Unauthorized or invalid grant/deny/revoke request from %s in room %s.\n", userID, roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "Unauthorized or invalid permission change request."})
			}
		case "request-files":
			// Send current shared files to the requesting client
			room.mutex.RLock()
			sharedFiles := make([]FileInfo, len(room.SharedFiles))
			copy(sharedFiles, room.SharedFiles)
			room.mutex.RUnlock()

			client.conn.WriteJSON(Message{
				Type:        "shared-files",
				SharedFiles: sharedFiles,
			})
		case "delete-file":
			// Only teacher can delete files
			if room.TeacherID == "" {
				log.Printf("Delete-file request received but no teacher in room %s. Ignoring.\n", roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "No teacher available to delete files."})
			} else if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can delete files."})
				break
			}

			if msg.FileID != "" {
				room.mutex.Lock()
				// Find and remove the file
				for i, file := range room.SharedFiles {
					if file.ID == msg.FileID {
						// Remove file from disk
						if err := os.Remove(file.Path); err != nil {
							log.Printf("Error deleting file %s: %v", file.Path, err)
						}
						// Remove from shared files
						room.SharedFiles = append(room.SharedFiles[:i], room.SharedFiles[i+1:]...)
						break
					}
				}
				room.mutex.Unlock()

				// Broadcast file deleted message
				broadcastToRoom(roomID, Message{
					Type:   "file-deleted",
					FileID: msg.FileID,
				}, userID)
			}
		case "screen_share_start":
			// Broadcast screen sharing started message
			log.Printf("User %s (%s) started screen sharing in room %s\n", username, userID, roomID)
			broadcastToRoom(roomID, Message{
				Type:     "screen_share_start",
				UserID:   userID,
				Username: username,
			}, userID)
		case "screen_share_stop":
			// Broadcast screen sharing stopped message
			log.Printf("User %s (%s) stopped screen sharing in room %s\n", username, userID, roomID)
			broadcastToRoom(roomID, Message{
				Type:     "screen_share_stop",
				UserID:   userID,
				Username: username,
			}, userID)
		case "request_screen_share_permission":
			// Student requesting screen sharing permission
			if room.TeacherID == "" {
				log.Printf("Screen share permission request received but no teacher in room %s. Ignoring.\n", roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "No teacher available to process screen share permission request."})
			} else if userID != room.TeacherID {
				teacherClient, found := func() (*Client, bool) {
					room.mutex.RLock()
					defer room.mutex.RUnlock()
					return room.clients[room.TeacherID], room.TeacherID != ""
				}()

				if found && teacherClient != nil {
					log.Printf("Student %s (%s) requested screen sharing permission in room %s. Relaying to teacher %s.\n", username, userID, roomID, room.TeacherID)
					err := teacherClient.conn.WriteJSON(Message{
						Type:           "screen_share_permission_request",
						TargetUserID:   userID,
						TargetUsername: username,
					})
					if err != nil {
						log.Println("Error relaying screen share permission request to teacher:", err)
					}
				} else {
					log.Printf("Teacher not found in room %s to relay screen share permission request from %s\n", roomID, userID)
					client.conn.WriteJSON(Message{Type: "error", Payload: "Teacher not found to process your request."})
				}
			} else {
				log.Printf("Teacher %s tried to request screen sharing permission (self-request). Ignoring.\n", userID)
			}
		case "screen_share_permission_response":
			// Teacher responding to screen sharing permission request
			if room.TeacherID == "" {
				log.Printf("Screen share permission response received but no teacher in room %s. Ignoring.\n", roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "No teacher available to process screen share permission."})
			} else if userID == room.TeacherID && msg.TargetUserID != "" {
				targetClient, found := func() (*Client, bool) {
					room.mutex.RLock()
					defer room.mutex.RUnlock()
					return room.clients[msg.TargetUserID], msg.TargetUserID != ""
				}()

				if found && targetClient != nil {
					// Check if the message has a 'granted' field from the frontend
					var messageType string
					var granted bool
					var ok bool

					if boolVal, isBool := msg.Payload.(bool); isBool {
						granted = boolVal
						ok = true
					} else if strVal, isString := msg.Payload.(string); isString {
						granted, err = strconv.ParseBool(strVal)
						if err != nil {
							log.Printf("ERROR: Could not parse string payload to bool: %v", err)
							client.conn.WriteJSON(Message{Type: "error", Payload: "Invalid payload format. Expected boolean or boolean string."})
							return
						}
						ok = true
					}

					if ok && granted {
						messageType = "screen_share_permission_granted"
						// Store the permission in the room
						room.mutex.Lock()
						room.GrantedScreenShareAccess[msg.TargetUserID] = true
						room.mutex.Unlock()
						log.Printf("Teacher %s granted screen sharing permission to student %s in room %s.\n", username, msg.TargetUserID, roomID)
					} else if ok {
						// Type assertion succeeded but value is false
						messageType = "screen_share_permission_denied"
						// Remove the permission from the room
						room.mutex.Lock()
						delete(room.GrantedScreenShareAccess, msg.TargetUserID)
						room.mutex.Unlock()
						log.Printf("Teacher %s denied screen sharing permission to student %s in room %s.\n", username, msg.TargetUserID, roomID)
					} else {
						// Type assertion failed - payload is not a boolean or string
						log.Printf("ERROR: Payload type assertion failed. Expected bool or string, got %T with value %+v", msg.Payload, msg.Payload)
						client.conn.WriteJSON(Message{Type: "error", Payload: "Invalid payload format. Expected boolean value."})
						return
					}

					err := targetClient.conn.WriteJSON(Message{
						Type: messageType,
					})
					if err != nil {
						log.Println("Error sending screen share permission response to student:", err)
					}
				} else {
					log.Printf("Target student %s not found in room %s to send screen share permission response\n", msg.TargetUserID, roomID)
				}
			} else if room.TeacherID != "" {
				log.Printf("Unauthorized or invalid screen share permission response from %s in room %s.\n", userID, roomID)
				client.conn.WriteJSON(Message{Type: "error", Payload: "Unauthorized or invalid permission response."})
			}
		case "start-recording":
			// Only teacher can start recording
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can start recording."})
				break
			}

			room.RecordingMutex.Lock()
			if room.RecordingActive {
				room.RecordingMutex.Unlock()
				client.conn.WriteJSON(Message{Type: "error", Payload: "Recording is already active."})
				break
			}

			// Create new recording session
			recordingID := uuid.New().String()
			recording := &RecordingSession{
				ID:              recordingID,
				RoomID:          roomID,
				StartTime:       time.Now(),
				TeacherID:       userID,
				TeacherUsername: username,
				Subject:         room.Subject,
				Status:          "active",
				WhiteboardData:  make([]Message, 0),
			}

			room.CurrentRecording = recording
			room.RecordingActive = true
			room.RecordingMutex.Unlock()

			log.Printf("Recording started by teacher %s in room %s. Recording ID: %s", username, roomID, recordingID)

			// Send recording ID back to client
			client.conn.WriteJSON(Message{
				Type:        "recording-started",
				RecordingID: recordingID,
				Timestamp:   time.Now().Unix(),
			})

			// Broadcast recording started message to other clients
			broadcastToRoom(roomID, Message{
				Type:        "recording-started",
				RecordingID: recordingID,
				Timestamp:   time.Now().Unix(),
			}, userID)

		case "stop-recording":
			// Only teacher can stop recording
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can stop recording."})
				break
			}

			room.RecordingMutex.Lock()
			if !room.RecordingActive || room.CurrentRecording == nil {
				room.RecordingMutex.Unlock()
				client.conn.WriteJSON(Message{Type: "error", Payload: "No active recording to stop."})
				break
			}

			// Finalize recording
			recording := room.CurrentRecording
			recording.EndTime = time.Now()
			recording.Duration = int64(recording.EndTime.Sub(recording.StartTime).Seconds())
			recording.Status = "completed"

			// Save recording data
			saveRecording(recording)

			room.RecordingActive = false
			room.CurrentRecording = nil
			room.RecordingMutex.Unlock()

			log.Printf("Recording stopped by teacher %s in room %s. Recording ID: %s", username, roomID, recording.ID)

			// Broadcast recording stopped message
			broadcastToRoom(roomID, Message{
				Type:        "recording-stopped",
				RecordingID: recording.ID,
				Timestamp:   time.Now().Unix(),
			}, "")

		case "recording-data":
			// Handle recording data from clients (video, audio, screen share)
			if room.RecordingActive && room.CurrentRecording != nil {
				room.RecordingMutex.Lock()
				recording := room.CurrentRecording

				// Add timestamp to the recording data
				msg.Timestamp = time.Now().Unix()

				// Store recording data based on type
				switch msg.RecordingType {
				case "whiteboard":
					// Whiteboard data is already handled in the drawing cases above
					// This is for additional whiteboard metadata
					recording.WhiteboardData = append(recording.WhiteboardData, msg)
				case "video":
					// Store video data reference
					if videoData, ok := msg.RecordingData.(map[string]interface{}); ok {
						if url, exists := videoData["url"]; exists {
							recording.VideoURL = url.(string)
						}
					}
				case "audio":
					// Store audio data reference
					if audioData, ok := msg.RecordingData.(map[string]interface{}); ok {
						if url, exists := audioData["url"]; exists {
							recording.AudioURL = url.(string)
						}
					}
				case "screen":
					// Store screen share data reference
					if screenData, ok := msg.RecordingData.(map[string]interface{}); ok {
						if url, exists := screenData["url"]; exists {
							recording.ScreenShareURL = url.(string)
						}
					}
				}

				room.RecordingMutex.Unlock()
			}

		case "get-recordings":
			// Get list of recordings for the room
			recordings := getRoomRecordings(roomID)
			client.conn.WriteJSON(Message{
				Type:       "recordings-list",
				Recordings: recordings,
			})

		case "load-recording":
			// Load a specific recording for playback
			if msg.RecordingID != "" {
				recording := loadRecording(msg.RecordingID)
				if recording != nil {
					client.conn.WriteJSON(Message{
						Type:        "recording-loaded",
						RecordingID: recording.ID,
						Payload:     recording,
					})
				} else {
					client.conn.WriteJSON(Message{Type: "error", Payload: "Recording not found."})
				}
			}

		// Quiz handling cases
		case "create-quiz":
			// Only teacher can create quizzes
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can create quizzes."})
				break
			}

			room.QuizMutex.Lock()
			quizID := uuid.New().String()
			quiz := Quiz{
				ID:          quizID,
				RoomID:      roomID,
				Title:       msg.QuizData.Title,
				Questions:   msg.QuizData.Questions,
				CreatedBy:   userID,
				CreatedAt:   time.Now(),
				Status:      "active",
				TimeLimit:   msg.QuizData.TimeLimit,
				TotalPoints: calculateTotalPoints(msg.QuizData.Questions),
			}
			room.Quizzes = append(room.Quizzes, quiz)
			room.QuizMutex.Unlock()

			log.Printf("Quiz created by teacher %s in room %s: %s", username, roomID, quiz.Title)

			// Broadcast quiz to all students
			broadcastToRoom(roomID, Message{
				Type:     "quiz-created",
				QuizData: quiz,
			}, userID)

		case "submit-quiz":
			// Students submit quiz answers
			if userID == room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Teachers cannot submit quizzes."})
				break
			}

			room.QuizMutex.Lock()
			// Find the quiz
			var quiz *Quiz
			for i := range room.Quizzes {
				if room.Quizzes[i].ID == msg.QuizID {
					quiz = &room.Quizzes[i]
					break
				}
			}

			if quiz == nil || quiz.Status != "active" {
				room.QuizMutex.Unlock()
				client.conn.WriteJSON(Message{Type: "error", Payload: "Quiz not found or not active."})
				break
			}

			// Create submission
			submission := QuizSubmission{
				ID:          uuid.New().String(),
				QuizID:      msg.QuizID,
				StudentID:   userID,
				StudentName: username,
				Answers:     msg.QuizAnswers,
				SubmittedAt: time.Now(),
				Score:       calculateScore(quiz.Questions, msg.QuizAnswers),
				MaxScore:    quiz.TotalPoints,
				Graded:      false,
			}
			room.QuizSubmissions = append(room.QuizSubmissions, submission)
			room.QuizMutex.Unlock()

			log.Printf("Quiz submitted by student %s in room %s", username, roomID)

			// Notify teacher of new submission
			teacherClient, found := func() (*Client, bool) {
				room.mutex.RLock()
				defer room.mutex.RUnlock()
				return room.clients[room.TeacherID], room.TeacherID != ""
			}()

			if found && teacherClient != nil {
				teacherClient.conn.WriteJSON(Message{
					Type:           "quiz-submitted",
					QuizSubmission: submission,
				})
			}

			// Confirm submission to student
			client.conn.WriteJSON(Message{
				Type:    "quiz-submission-confirmed",
				Payload: "Quiz submitted successfully",
			})

		case "grade-quiz":
			// Only teacher can grade quizzes
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can grade quizzes."})
				break
			}

			room.QuizMutex.Lock()
			// Find and update submission
			for i := range room.QuizSubmissions {
				if room.QuizSubmissions[i].ID == msg.QuizSubmission.ID {
					room.QuizSubmissions[i].Score = msg.QuizSubmission.Score
					room.QuizSubmissions[i].Feedback = msg.QuizSubmission.Feedback
					room.QuizSubmissions[i].Graded = true
					break
				}
			}
			room.QuizMutex.Unlock()

			// Notify student of graded quiz
			studentClient, found := func() (*Client, bool) {
				room.mutex.RLock()
				defer room.mutex.RUnlock()
				return room.clients[msg.QuizSubmission.StudentID], msg.QuizSubmission.StudentID != ""
			}()

			if found && studentClient != nil {
				studentClient.conn.WriteJSON(Message{
					Type:           "quiz-graded",
					QuizSubmission: msg.QuizSubmission,
				})
			}

		case "get-quiz-submissions":
			// Only teacher can view all submissions
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can view quiz submissions."})
				break
			}

			room.QuizMutex.Lock()
			submissions := make([]QuizSubmission, len(room.QuizSubmissions))
			copy(submissions, room.QuizSubmissions)
			room.QuizMutex.Unlock()

			client.conn.WriteJSON(Message{
				Type:        "quiz-submissions",
				Submissions: submissions,
			})

		case "get-my-quiz-results":
			// Students can view their own results
			room.QuizMutex.Lock()
			var myResults []QuizSubmission
			for _, submission := range room.QuizSubmissions {
				if submission.StudentID == userID && submission.Graded {
					myResults = append(myResults, submission)
				}
			}
			room.QuizMutex.Unlock()

			client.conn.WriteJSON(Message{
				Type:        "my-quiz-results",
				Submissions: myResults,
			})

		case "close-quiz":
			// Only teacher can close quizzes
			if userID != room.TeacherID {
				client.conn.WriteJSON(Message{Type: "error", Payload: "Only the teacher can close quizzes."})
				break
			}

			room.QuizMutex.Lock()
			for i := range room.Quizzes {
				if room.Quizzes[i].ID == msg.QuizID {
					room.Quizzes[i].Status = "closed"
					break
				}
			}
			room.QuizMutex.Unlock()

			// Broadcast quiz closed to all students
			broadcastToRoom(roomID, Message{
				Type:   "quiz-closed",
				QuizID: msg.QuizID,
			}, userID)

		case "get-active-quizzes":
			// Students can request active quizzes
			room.QuizMutex.Lock()
			var activeQuizzes []Quiz
			for _, quiz := range room.Quizzes {
				if quiz.Status == "active" {
					activeQuizzes = append(activeQuizzes, quiz)
				}
			}
			room.QuizMutex.Unlock()

			// Send active quizzes to the requesting client
			for _, quiz := range activeQuizzes {
				client.conn.WriteJSON(Message{
					Type:     "quiz-created",
					QuizData: quiz,
				})
			}
		default:
			if msg.Type == "set-subject" {
				if room.TeacherID == "" {
					log.Printf("Set-subject request received but no teacher in room %s. Ignoring.\n", roomID)
					client.conn.WriteJSON(Message{Type: "error", Payload: "No teacher available to set subject."})
				} else if userID == room.TeacherID {
					// Teacher sets/updates subject
					if subj, ok := msg.Payload.(string); ok {
						room.mutex.Lock()
						room.Subject = subj
						room.mutex.Unlock()
						log.Printf("Subject updated by teacher %s in room %s: %s\n", userID, roomID, subj)
						broadcastToRoom(roomID, Message{Type: "subject-updated", Subject: subj}, "")
					} else {
						// Fallback: if client sends in msg.Subject
						if msg.Subject != "" {
							room.mutex.Lock()
							room.Subject = msg.Subject
							room.mutex.Unlock()
							log.Printf("Subject updated by teacher %s in room %s: %s\n", userID, roomID, msg.Subject)
							broadcastToRoom(roomID, Message{Type: "subject-updated", Subject: msg.Subject}, "")
						}
					}
				}
			} else {
				log.Printf("Unknown message type received: %s from client %s\n", msg.Type, userID)
			}
		}
	}

	// Client disconnected, remove from room
	room.mutex.Lock()
	delete(room.clients, userID)

	// If the room is now empty, save the whiteboard history and any active recording, then delete the room
	if len(room.clients) == 0 {
		log.Printf("Room %s is now empty. Saving whiteboard history and recordings...", roomID)
		saveRoomHistory(roomID, room.DrawingHistory)

		// Save any active recording before deleting the room
		if room.RecordingActive && room.CurrentRecording != nil {
			room.RecordingMutex.Lock()
			recording := room.CurrentRecording
			recording.EndTime = time.Now()
			recording.Duration = int64(recording.EndTime.Sub(recording.StartTime).Seconds())
			recording.Status = "completed"
			saveRecording(recording)
			room.RecordingMutex.Unlock()
			log.Printf("Active recording %s saved before room deletion", recording.ID)
		}

		// Save quiz data before deleting the room
		if len(room.Quizzes) > 0 || len(room.QuizSubmissions) > 0 {
			room.QuizMutex.Lock()
			saveQuizData(roomID, room.Quizzes, room.QuizSubmissions)
			room.QuizMutex.Unlock()
		}

		roomsMutex.Lock()
		delete(rooms, roomID)
		roomsMutex.Unlock()
		log.Printf("Room %s deleted.", roomID)
	} else if userID == room.TeacherID {
		// Save any active recording before teacher leaves
		if room.RecordingActive && room.CurrentRecording != nil {
			room.RecordingMutex.Lock()
			recording := room.CurrentRecording
			recording.EndTime = time.Now()
			recording.Duration = int64(recording.EndTime.Sub(recording.StartTime).Seconds())
			recording.Status = "completed"
			saveRecording(recording)
			room.RecordingActive = false
			room.CurrentRecording = nil
			room.RecordingMutex.Unlock()
			log.Printf("Active recording %s saved when teacher left", recording.ID)
		}

		room.TeacherID = ""
		log.Printf("Teacher %s (%s) left room %s. Room has no teacher now.\n", username, userID, roomID)

		// Auto-assign a new teacher if there are still clients in the room
		if len(room.clients) > 0 {
			// Pick the first available client as the new teacher
			for newTeacherID, newTeacherClient := range room.clients {
				room.TeacherID = newTeacherID
				log.Printf("Auto-assigned %s (%s) as new teacher in room %s\n", newTeacherClient.username, newTeacherID, roomID)

				// Notify the new teacher
				newTeacherClient.conn.WriteJSON(Message{
					Type:     "teacher-assigned",
					UserID:   newTeacherID,
					Username: newTeacherClient.username,
				})

				// Broadcast to all clients about the new teacher
				broadcastToRoom(roomID, Message{
					Type:      "teacher-changed",
					TeacherID: newTeacherID,
					Username:  newTeacherClient.username,
				}, "")
				break
			}
		}
	}
	delete(room.GrantedWhiteboardAccess, userID)
	room.mutex.Unlock()

	broadcastToRoom(roomID, Message{
		Type:          "user-left",
		UserID:        userID,
		Username:      username,
		CurrentRoster: getRoomRoster(room),
	}, userID)

	if len(room.clients) == 0 {
		roomsMutex.Lock()
		delete(rooms, roomID)
		roomsMutex.Unlock()
		log.Printf("Room %s is now empty and removed.\n", roomID)
	}
}

// broadcastToRoom sends a message to all clients in a given room, excluding the sender.
func broadcastToRoom(roomID string, message Message, senderUserID string) {
	roomsMutex.RLock()
	room, ok := rooms[roomID]
	roomsMutex.RUnlock()

	if !ok {
		log.Printf("Attempted to broadcast to non-existent room: %s\n", roomID)
		return
	}

	room.mutex.RLock()
	defer room.mutex.RUnlock()

	for _, client := range room.clients {
		if senderUserID == "" || client.userID != senderUserID {
			err := client.conn.WriteJSON(message)
			if err != nil {
				log.Printf("Write error to client %s in room %s: %v\n", client.userID, roomID, err)
			}
		}
	}
}

// relayToClient sends a message to a specific client within a room.
func relayToClient(roomID string, targetUserID string, message Message) {
	roomsMutex.RLock()
	room, ok := rooms[roomID]
	roomsMutex.RUnlock()

	if !ok {
		log.Printf("Attempted to relay to non-existent room: %s\n", roomID)
		return
	}

	room.mutex.RLock()
	defer room.mutex.RUnlock()

	if client, ok := room.clients[targetUserID]; ok {
		err := client.conn.WriteJSON(message)
		if err != nil {
			log.Printf("Write error to target client %s in room %s: %v\n", targetUserID, roomID, err)
		}
	} else {
		log.Printf("Target client %s not found in room %s for relaying message type %s\n", targetUserID, roomID, message.Type)
	}
}

// fileUploadHandler handles file uploads
func fileUploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (32MB max)
	err := r.ParseMultipartForm(32 << 20)
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Get room ID and user info
	roomID := r.FormValue("roomId")
	userID := r.FormValue("userId")
	username := r.FormValue("username")

	if roomID == "" || userID == "" || username == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Get the uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create uploads directory if it doesn't exist
	uploadsDir := "uploads"
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		http.Error(w, "Failed to create uploads directory", http.StatusInternalServerError)
		return
	}

	// Create room-specific directory
	roomDir := filepath.Join(uploadsDir, roomID)
	if err := os.MkdirAll(roomDir, 0755); err != nil {
		http.Error(w, "Failed to create room directory", http.StatusInternalServerError)
		return
	}

	// Generate unique filename
	fileID := uuid.New().String()
	ext := filepath.Ext(header.Filename)
	filename := fileID + ext
	filePath := filepath.Join(roomDir, filename)

	// Create the file
	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy file content
	_, err = io.Copy(dst, file)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Determine file type
	fileType := header.Header.Get("Content-Type")
	isImage := strings.HasPrefix(fileType, "image/")
	isPDF := fileType == "application/pdf"
	isDocument := strings.HasPrefix(fileType, "application/") &&
		(strings.Contains(fileType, "word") || strings.Contains(fileType, "excel") ||
			strings.Contains(fileType, "powerpoint") || strings.Contains(fileType, "document"))

	// Create file info
	fileInfo := FileInfo{
		ID:         fileID,
		Name:       header.Filename,
		Size:       header.Size,
		Type:       fileType,
		UploadedBy: username,
		UploadedAt: time.Now(),
		Path:       filePath,
		IsImage:    isImage,
		IsPDF:      isPDF,
		IsDocument: isDocument,
	}

	// Add file to room
	roomsMutex.Lock()
	room, exists := rooms[roomID]
	if exists {
		room.SharedFiles = append(room.SharedFiles, fileInfo)
	}
	roomsMutex.Unlock()

	// Broadcast file shared message
	if exists {
		message := Message{
			Type:           "file-shared",
			FileID:         fileInfo.ID,
			FileName:       fileInfo.Name,
			FileSize:       fileInfo.Size,
			FileType:       fileInfo.Type,
			FileUploadedBy: fileInfo.UploadedBy,
			FileUploadedAt: fileInfo.UploadedAt.Format(time.RFC3339),
		}
		broadcastToRoom(roomID, message, userID)
	}

	// Return success response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"file":    fileInfo,
	})
}

// fileDownloadHandler serves uploaded files
func fileDownloadHandler(w http.ResponseWriter, r *http.Request) {
	// Extract room ID and filename from URL path
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 4 {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	roomID := pathParts[2]
	filename := pathParts[3]
	filePath := filepath.Join("uploads", roomID, filename)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// handleGetRecording serves the recorded whiteboard history for a given roomID.
func handleGetRecording(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Path[len("/recordings/"):]
	if roomID == "" {
		http.Error(w, "Room ID is required", http.StatusBadRequest)
		return
	}

	filePath := "backend/recordings/" + roomID + ".json"

	// Check if the file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Recording not found", http.StatusNotFound)
		log.Printf("Recording not found for room ID: %s", roomID)
		return
	}

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// handleGetRecordingFile serves a specific recording file by ID
func handleGetRecordingFile(w http.ResponseWriter, r *http.Request) {
	// Extract recording ID from URL path: /recording/{recordingID}
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 3 {
		http.Error(w, "Recording ID is required", http.StatusBadRequest)
		return
	}

	recordingID := pathParts[2]
	if recordingID == "" {
		http.Error(w, "Recording ID is required", http.StatusBadRequest)
		return
	}

	filePath := filepath.Join("backend/recordings", recordingID+".json")

	// Check if the file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Recording not found", http.StatusNotFound)
		log.Printf("Recording not found for ID: %s", recordingID)
		return
	}

	// Set CORS headers for cross-origin requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// handleGetRecordingMedia serves a specific recording media file (video, audio, screen share) by ID
func handleGetRecordingMedia(w http.ResponseWriter, r *http.Request) {
	// Extract recording ID and media type from URL path: /recording-media/{recordingID}/{type}
	pathParts := strings.Split(r.URL.Path, "/")
	if len(pathParts) < 4 {
		http.Error(w, "Recording ID and media type are required", http.StatusBadRequest)
		return
	}

	recordingID := pathParts[2]
	mediaType := pathParts[3]

	if recordingID == "" || mediaType == "" {
		http.Error(w, "Recording ID and media type are required", http.StatusBadRequest)
		return
	}

	// Construct the correct filename with the _recording.webm suffix
	filename := fmt.Sprintf("%s_recording.webm", mediaType)
	filePath := filepath.Join("backend/recordings", recordingID, filename)

	// Check if the file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "Recording media not found", http.StatusNotFound)
		log.Printf("Recording media not found for ID: %s, type: %s, path: %s", recordingID, mediaType, filePath)
		return
	}

	// Set CORS headers for cross-origin requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Serve the file
	http.ServeFile(w, r, filePath)
}

// handleRecordingFileUpload handles file uploads for recording media (video, audio, screen share)
func handleRecordingFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form (32MB max)
	err := r.ParseMultipartForm(32 << 20)
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Get recording info
	recordingID := r.FormValue("recordingId")
	recordingType := r.FormValue("recordingType")
	roomID := r.FormValue("roomId")

	if recordingID == "" || recordingType == "" || roomID == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	// Get the uploaded file
	file, _, err := r.FormFile("mediaFile")
	if err != nil {
		http.Error(w, "Failed to get file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create recordings directory if it doesn't exist
	recordingsDir := "backend/recordings"
	if err := os.MkdirAll(recordingsDir, 0755); err != nil {
		http.Error(w, "Failed to create recordings directory", http.StatusInternalServerError)
		return
	}

	// Create recording-specific directory
	recordingDir := filepath.Join(recordingsDir, recordingID)
	if err := os.MkdirAll(recordingDir, 0755); err != nil {
		http.Error(w, "Failed to create recording directory", http.StatusInternalServerError)
		return
	}

	// Save the media file
	filename := fmt.Sprintf("%s_recording.webm", recordingType)
	filePath := filepath.Join(recordingDir, filename)

	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Failed to create file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy file content
	_, err = io.Copy(dst, file)
	if err != nil {
		http.Error(w, "Failed to save file", http.StatusInternalServerError)
		return
	}

	// Update the recording session with the media URL
	recording := loadRecording(recordingID)
	if recording != nil {
		// Update the appropriate URL field based on recording type
		switch recordingType {
		case "video":
			recording.VideoURL = fmt.Sprintf("/recording-media/%s/video", recordingID)
		case "audio":
			recording.AudioURL = fmt.Sprintf("/recording-media/%s/audio", recordingID)
		case "screen":
			recording.ScreenShareURL = fmt.Sprintf("/recording-media/%s/screen", recordingID)
		case "composite":
			// Composite recordings contain video, audio, and potentially screen sharing
			recording.VideoURL = fmt.Sprintf("/recording-media/%s/composite", recordingID)
			recording.AudioURL = fmt.Sprintf("/recording-media/%s/composite", recordingID)
			// If screen sharing was part of the composite, also set screen share URL
			if strings.Contains(filename, "screen") {
				recording.ScreenShareURL = fmt.Sprintf("/recording-media/%s/composite", recordingID)
			}
		case "whiteboard":
			// Whiteboard-only recordings don't have video/audio URLs
			log.Printf("Whiteboard-only recording uploaded for %s", recordingID)
		}

		// Save the updated recording
		saveRecording(recording)
		log.Printf("Updated recording %s with %s URL: %s", recordingID, recordingType, filePath)
	}

	// Return success response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("%s recording uploaded successfully", recordingType),
		"filePath": filePath,
	})
}

// saveRoomHistory saves the drawing history of a room to a JSON file
func saveRoomHistory(roomID string, history []Message) {
	roomsMutex.RLock()
	_, exists := rooms[roomID]
	roomsMutex.RUnlock()

	if !exists || len(history) == 0 {
		log.Printf("No history to save for room %s or room does not exist.", roomID)
		return
	}

	filePath := "backend/recordings/" + roomID + ".json"

	file, err := os.Create(filePath)
	if err != nil {
		log.Printf("Error creating history file for room %s: %v", roomID, err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ") // Pretty print JSON
	if err := encoder.Encode(history); err != nil {
		log.Printf("Error encoding history for room %s: %v", roomID, err)
		return
	}

	log.Printf("Successfully saved history for room %s to %s", roomID, filePath)
}

// saveRecording saves a recording session to a JSON file
func saveRecording(recording *RecordingSession) {
	// Create recordings directory if it doesn't exist
	recordingsDir := "backend/recordings"
	if err := os.MkdirAll(recordingsDir, 0755); err != nil {
		log.Printf("Error creating recordings directory: %v", err)
		return
	}

	filePath := filepath.Join(recordingsDir, recording.ID+".json")
	file, err := os.Create(filePath)
	if err != nil {
		log.Printf("Error creating recording file for %s: %v", recording.ID, err)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ") // Pretty print JSON
	if err := encoder.Encode(recording); err != nil {
		log.Printf("Error encoding recording for %s: %v", recording.ID, err)
		return
	}

	log.Printf("Successfully saved recording %s to %s", recording.ID, filePath)
}

// loadRecording loads a recording session from a JSON file
func loadRecording(recordingID string) *RecordingSession {
	filePath := filepath.Join("backend/recordings", recordingID+".json")

	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("Error opening recording file for %s: %v", recordingID, err)
		return nil
	}
	defer file.Close()

	var recording RecordingSession
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&recording); err != nil {
		log.Printf("Error decoding recording for %s: %v", recordingID, err)
		return nil
	}

	return &recording
}

// getRoomRecordings gets all recordings for a specific room
func getRoomRecordings(roomID string) []RecordingSession {
	recordingsDir := "backend/recordings"
	files, err := os.ReadDir(recordingsDir)
	if err != nil {
		log.Printf("Error reading recordings directory: %v", err)
		return []RecordingSession{}
	}

	var recordings []RecordingSession
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(recordingsDir, file.Name())
		fileHandle, err := os.Open(filePath)
		if err != nil {
			log.Printf("Error opening recording file %s: %v", file.Name(), err)
			continue
		}

		// Try to decode as RecordingSession first
		var recording RecordingSession
		decoder := json.NewDecoder(fileHandle)
		if err := decoder.Decode(&recording); err != nil {
			// If it fails, it might be an old whiteboard-only file
			fileHandle.Close()
			fileHandle, err = os.Open(filePath)
			if err != nil {
				continue
			}

			// Try to decode as whiteboard data array
			var whiteboardData []Message
			decoder = json.NewDecoder(fileHandle)
			if err := decoder.Decode(&whiteboardData); err != nil {
				log.Printf("Error decoding file %s as either RecordingSession or whiteboard data: %v", file.Name(), err)
				fileHandle.Close()
				continue
			}

			// Create a RecordingSession from old whiteboard data
			recordingID := strings.TrimSuffix(file.Name(), ".json")
			recording = RecordingSession{
				ID:              recordingID,
				RoomID:          recordingID,                  // Old files used roomID as filename
				StartTime:       time.Now().AddDate(0, 0, -1), // Placeholder date
				Status:          "completed",
				WhiteboardData:  whiteboardData,
				TeacherUsername: "Unknown",
				Subject:         "Legacy Recording",
			}
		}
		fileHandle.Close()

		// Only include recordings for this room
		if recording.RoomID == roomID {
			recordings = append(recordings, recording)
		}
	}

	return recordings
}

func main() {
	fmt.Println("Starting Aisha Classroom Server...")

	// Create uploads directory
	if err := os.MkdirAll("uploads", 0755); err != nil {
		log.Fatal("Failed to create uploads directory:", err)
	}

	// Create recordings directory
	if err := os.MkdirAll("backend/recordings", 0755); err != nil {
		log.Fatal("Failed to create recordings directory:", err)
	}

	// Create quizzes directory
	if err := os.MkdirAll("backend/quizzes", 0755); err != nil {
		log.Fatal("Failed to create quizzes directory:", err)
	}

	// Set up routes
	http.HandleFunc("/ws", wsHandler)
	http.HandleFunc("/upload", fileUploadHandler)
	http.HandleFunc("/files/", fileDownloadHandler)
	http.HandleFunc("/recordings/", handleGetRecording)
	http.HandleFunc("/recording/", handleGetRecordingFile)
	http.HandleFunc("/recording-media/", handleGetRecordingMedia)
	http.HandleFunc("/upload-recording-media/", handleRecordingFileUpload)

	// Serve static files from frontend directory
	fs := http.FileServer(http.Dir("../frontend/"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Explicit routes for HTML files (must be before catch-all)
	http.HandleFunc("/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "../frontend/index.html")
	})
	http.HandleFunc("/quiz-demo.html", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "../frontend/quiz-demo.html")
	})
	http.HandleFunc("/playback.html", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "../frontend/playback.html")
	})

	// Serve welcome page as default, but allow other files to be served directly
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "../frontend/index.html")
		} else {
			// Try to serve file from frontend directory
			filePath := "../frontend" + r.URL.Path
			if _, err := os.Stat(filePath); err == nil {
				http.ServeFile(w, r, filePath)
			} else {
				http.NotFound(w, r)
			}
		}
	})

	fmt.Println("Server starting on :8888")
	fmt.Println("Homepage: http://localhost:8888/")
	fmt.Println("File upload endpoint: /upload")
	fmt.Println("File download endpoint: /files/{roomId}/{filename}")

	err := http.ListenAndServe(":8888", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
