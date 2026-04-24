package main

import (
	"context"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chai2010/webp"
	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/golang-jwt/jwt/v4"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var jwtSecretKey []byte

// const (
// 	host     = "localhost"      // or the Docker service name if running in another container
// 	port     = 5432             // default PostgreSQL port
// 	user     = "admin"  // as defined in docker-compose.yml
// 	password = "password" // as defined in docker-compose.yml
// 	dbname   = "hotsheet"       // as defined in docker-compose.yml
// )

func getJWTFromHeaderOrCookie(c *fiber.Ctx) string {
	authHeader := c.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			return parts[1]
		}
	}
	return ""
}

func authRequired(c *fiber.Ctx) error {
	tokenString := getJWTFromHeaderOrCookie(c)
	log.Printf("Received JWT token: %s", tokenString)

	if tokenString == "" {
		log.Println("No JWT token found in header or cookies")
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return jwtSecretKey, nil
	})

	if err != nil {
		log.Printf("Error parsing token: %v", err)
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	if !token.Valid {
		log.Println("Token is invalid")
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	c.Locals("jwt", token)
	log.Printf("JWT Payload: %+v", claims)

	if userID, ok := claims["user_id"].(float64); ok {
		log.Printf("Authenticated user ID: %v", userID)
	} else {
		log.Printf("User ID not found in claims or invalid type")
	}

	if contactIDs, ok := claims["contact_ids"].([]interface{}); ok {
		log.Printf("Contact IDs: %v", contactIDs)
	} else {
		log.Printf("Contact IDs not found in claims or invalid type")
	}

	return c.Next()
}

// ระบบ block IP (in-memory, block ชั่วคราว 1 วัน)
var blockedIPs = make(map[string]time.Time)

func blockIP(ip string, duration time.Duration) {
	blockedIPs[ip] = time.Now().Add(duration)
}

func isIPBlocked(ip string) bool {
	expire, exists := blockedIPs[ip]
	if !exists {
		return false
	}
	if time.Now().After(expire) {
		delete(blockedIPs, ip) // auto-unban
		return false
	}
	return true
}

func blocklistMiddleware(c *fiber.Ctx) error {
	ip := c.IP()
	if isIPBlocked(ip) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error": "Your IP has been temporarily blocked.",
		})
	}
	return c.Next()
}

// Middleware: เช็คว่า jwtSecretKey ถูกตั้งค่าหรือยัง
func checkSecretKeyMiddleware(c *fiber.Ctx) error {
	if len(jwtSecretKey) == 0 {
		ip := c.IP()
		log.Printf("SECRET_KEY is not set! (jwtSecretKey is empty) | Request from IP: %s", ip)
		blockIP(ip, 24*time.Hour) // block IP นี้ 1 วัน
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Server misconfiguration: SECRET_KEY is missing",
		})
	}
	return c.Next()
}

// สร้าง middleware สำหรับ rate limit โดยเฉพาะ
func setupRateLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        30,              // จำนวนครั้งสูงสุดที่อนุญาต
		Expiration: 1 * time.Minute, // ระยะเวลาที่จะ reset
		KeyGenerator: func(c *fiber.Ctx) string {
			// ใช้ IP address เป็น key สำหรับการนับ rate limit
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "Rate limit exceeded. Please try again later.",
				"wait":  "1 minute",
			})
		},
		// สามารถเพิ่ม storage แบบอื่นได้ เช่น Redis
		// Storage: store,
	})
}

// Global function to load the .env file once at the start of the application
func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Println(".env file not found, using environment variables")
	}
}

// ฟังก์ชันโหลดค่าการตั้งค่า Cloudinary
func setupCloudinary() (*cloudinary.Cloudinary, context.Context, error) {
	// ดึงค่า CLOUDINARY_URL จาก environment
	cloudinaryURL := os.Getenv("CLOUDINARY_URL")
	if cloudinaryURL == "" {
		log.Fatal("❌ ไม่พบค่าของ CLOUDINARY_URL")
	}

	// ตั้งค่า Cloudinary
	cld, err := cloudinary.NewFromURL(cloudinaryURL)
	if err != nil {
		return nil, nil, err
	}
	cld.Config.URL.Secure = true
	ctx := context.Background()
	return cld, ctx, nil
}

// Add a function to validate the file extension
func isValidFileExtension(filename string) bool {
	validExtensions := []string{".jpg", ".jpeg", ".png", ".gif"}
	ext := filepath.Ext(filename)
	for _, validExt := range validExtensions {
		if strings.ToLower(ext) == validExt {
			return true
		}
	}
	return false
}

// Add a function to validate file size (max 5MB in this case)
func isValidFileSize(fileSize int64) bool {
	const maxSize = 5 * 1024 * 1024 // 5MB
	return fileSize <= maxSize
}

// ฟังก์ชันสำหรับแปลงภาพเป็น WebP
func convertToWebP(file *multipart.FileHeader, quality float32) ([]byte, error) {
	// เปิดไฟล์
	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer src.Close()

	// decode ภาพ
	var img image.Image
	var decodeErr error

	// ตรวจสอบนามสกุลไฟล์
	ext := filepath.Ext(file.Filename)
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg":
		img, decodeErr = jpeg.Decode(src)
	case ".png":
		img, decodeErr = png.Decode(src)
	case ".gif":
		img, decodeErr = gif.Decode(src)
	default:
		return nil, fmt.Errorf("unsupported image format: %s", ext)
	}

	if decodeErr != nil {
		return nil, fmt.Errorf("failed to decode image: %v", decodeErr)
	}

	// แปลงเป็น WebP
	var webpData []byte
	webpData, err = webp.EncodeRGBA(img, quality)
	if err != nil {
		return nil, fmt.Errorf("failed to encode WebP: %v", err)
	}

	return webpData, nil
}

// ฟังก์ชันสำหรับบันทึกไฟล์ WebP
func saveWebPFile(webpData []byte, filename string, uploadDir string) error {
	// สร้างชื่อไฟล์ WebP
	webpFilename := strings.TrimSuffix(filename, filepath.Ext(filename)) + ".webp"
	savePath := filepath.Join(uploadDir, webpFilename)

	// บันทึกไฟล์
	err := os.WriteFile(savePath, webpData, 0644)
	if err != nil {
		return fmt.Errorf("failed to save WebP file: %v", err)
	}

	return nil
}

func main() {
	// Load .env file globally
	loadEnv()

	// โหลด SECRET_KEY จาก env
	jwtSecretKey = []byte(os.Getenv("SECRET_KEY"))

	// // ตั้งค่า Cloudinary
	// cld, ctx, err := setupCloudinary()
	// if err != nil {
	// 	log.Fatal("❌ ตั้งค่า Cloudinary ไม่สำเร็จ:", err)
	// }

	// PostgreSQL connect on cloud
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=require",
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_NAME"),
	)

	// Detailed SQL logger
	newLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold: time.Second,
			LogLevel:      logger.Info,
			Colorful:      true,
		},
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: newLogger,
	})

	if err != nil {
		panic("failed to connect to database")
	}

	// // PostgreSQL connect by docker
	// dsn := fmt.Sprintf("host=%s port=%d user=%s "+
	// 	"password=%s dbname=%s sslmode=disable",
	// 	host, port, user, password, dbname)

	// // Detailed SQL logger
	// newLogger := logger.New(
	// 	log.New(os.Stdout, "\r\n", log.LstdFlags),
	// 	logger.Config{
	// 		SlowThreshold: time.Second,
	// 		LogLevel:      logger.Info,
	// 		Colorful:      true,
	// 	},
	// )

	// db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
	// 	Logger: newLogger,
	// })

	// if err != nil {
	// 	panic("failed to connect to database")
	// }

	// Automigrate models
	db.AutoMigrate(&User{}, &Contact{}, &Platform{})

	// Setup Fiber
	app := fiber.New(fiber.Config{
		BodyLimit: 10 * 1024 * 1024, // จำกัดขนาด request body ที่ 10MB
	})

	app.Static("/uploads", "./uploads")

	// Rate limit middleware สำหรับ route ที่เกี่ยวกับรูปภาพเท่านั้น
	imageRateLimiter := setupRateLimiter()

	// ใช้ blocklistMiddleware ก่อน middleware อื่น ๆ
	app.Use(blocklistMiddleware)
	app.Use(checkSecretKeyMiddleware)

	// Or extend your config for customization
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000,https://enterlink.vercel.app",
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, PUT, DELETE, OPTIONS",
		AllowCredentials: true,
		ExposeHeaders:    "Set-Cookie",
		MaxAge:           3600,
	}))

	// Define Routes
	app.Post("/register", imageRateLimiter, func(c *fiber.Ctx) error {
		return register(db, c)
	})
	app.Post("/login", imageRateLimiter, func(c *fiber.Ctx) error {
		return login(db, c)
	})

	// Add Cloudinary context to the routes that need it
	app.Post("/profile", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		// Pass both db and Cloudinary context to the handler
		return updateUserProfileAndContacts(db, c)
	})

	app.Delete("/profile/platform", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return deletePlatform(db, c)
	})
	app.Put("/profile", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return UpdateContact(db, c)
	})

	app.Post("/profile/component", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return updateContactComponentIDs(db, c)
	})

	app.Get("/profile", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return getUserByID(db, c)
	})

	app.Get("/user/status", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return getUserStatus(db, c)
	})

	app.Post("/profile/image", authRequired, imageRateLimiter, func(c *fiber.Ctx) error {
		return updateContactImageLocal(db, c)
	})

	app.Get("/:domain", imageRateLimiter, func(c *fiber.Ctx) error {
		return getUserByDomain(db, c)
	})

	app.Get("/user/profile/:id", imageRateLimiter, func(c *fiber.Ctx) error {
		return fetchUserByID(db, c)
	})

	// ใหม่: ดึง domain ของผู้ใช้ทุกคน
	app.Get("/users/domains", imageRateLimiter, func(c *fiber.Ctx) error {
		return getAllUserDomains(db, c)
	})

	log.Fatal(app.Listen(":8000"))
}
