package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"os"
	"time"

	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/cloudinary/cloudinary-go/v2/api"
	"github.com/cloudinary/cloudinary-go/v2/api/uploader"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Request struct for updating component IDs
type UpdateComponentIDsRequest struct {
	UserID       uint   `json:"user_id" validate:"required"`
	ContactID    uint   `json:"contact_id" validate:"required"`
	ComponentIDs []uint `json:"component_ids" validate:"required"`
}

type User struct {
	gorm.Model
	Domain             string    `gorm:"uniqueIndex;not null" json:"domain" validate:"required,excludesrune=ก-๙"`
	Email              string    `gorm:"uniqueIndex;not null" json:"email" validate:"required,email"`
	Phone              string    `gorm:"not null" json:"phone" validate:"required,numeric,max=10"`
	Password           string    `gorm:"not null" json:"password" validate:"required,min=6"`
	FormOneCompleted   bool      `gorm:"default:false" json:"form_one_completed"`
	FormTwoCompleted   bool      `gorm:"default:false" json:"form_two_completed"`
	FormThreeCompleted bool      `gorm:"default:false" json:"form_three_completed"`
	Contacts           []Contact `gorm:"foreignKey:UserID" json:"contacts"`
}

// Custom type for ComponentIDs
type ComponentIDs []uint

// Implement the Scanner interface for custom type
func (c *ComponentIDs) Scan(value interface{}) error {
	if value == nil {
		*c = ComponentIDs{}
		return nil
	}

	// Convert the value to []byte
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("failed to convert component_ids to []byte")
	}

	// Unmarshal JSON data into []uint
	return json.Unmarshal(bytes, c)
}

// Implement  the Valuer interface for custom type
func (c ComponentIDs) Value() (driver.Value, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}

// Validator instance
var validate = validator.New()

func register(db *gorm.DB, c *fiber.Ctx) error {
	var user User

	// Parse request body
	if err := c.BodyParser(&user); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Validate request
	if err := validate.Struct(&user); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   "User validation failed",
			"details": err.Error(),
		})
	}

	// Encrypt password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to encrypt password",
		})
	}
	user.Password = string(hashedPassword)

	// Create user record in database
	if err := db.Create(&user).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to create user",
		})
	}

	// Return successful response
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"user":    user,
		"message": "User created successfully",
	})
}

type Contact struct {
	ID           uint         `gorm:"primaryKey"`
	Name         string       `json:"name"`
	Bio          string       `json:"bio"`
	UserID       uint         `gorm:"not null;index" json:"user_id"`
	ImageProfile string       `json:"image_profile"` // เพิ่ม field นี้
	ImageCover   string       `json:"image_cover"`   // เพิ่ม field นี้
	Platforms    []Platform   `gorm:"foreignKey:ContactID" json:"platforms"`
	ComponentIDs ComponentIDs `gorm:"type:jsonb" json:"component_ids"` // ใช้ custom type
}

// type Platform struct {
// 	ID         uint   `gorm:"primaryKey"`
// 	ContactID  uint   `gorm:"not null;index"`
// 	PlatformID uint   `json:"platform_id" validate:"required"` // เปลี่ยนจาก Platform เป็น PlatformID
// 	URL        string `json:"url" validate:"required,url"`
// 	Title      string `json:"title"`
// }

type Platform struct {
	ID         uint   `gorm:"primaryKey" json:"platform_number"`
	ContactID  uint   `gorm:"not null;index"`
	PlatformID uint   `json:"platform_id" validate:"required"` // เปลี่ยนจาก Platform เป็น PlatformID
	URL        string `json:"url" validate:"required,url"`
	Title      string `json:"title"`
}

func UpdateContact(db *gorm.DB, c *fiber.Ctx) error {
	// รับค่า user ID จาก JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	type RequestBody struct {
		Contacts []Contact `json:"contacts"`
	}

	var req RequestBody
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request body"})
	}

	// ดึง contact_id ทั้งหมดของ user
	var existingContacts []Contact
	if err := db.Where("user_id = ?", uint(userID)).Find(&existingContacts).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch existing contacts"})
	}

	// สร้าง map เก็บ contact_id ที่มีอยู่
	existingContactMap := make(map[uint]Contact)
	for _, contact := range existingContacts {
		existingContactMap[contact.ID] = contact
	}

	// ตรวจสอบและอัปเดต contacts
	for _, updatedContact := range req.Contacts {
		// ถ้าไม่มี contact_id หรือ contact_id ไม่มีอยู่ในระบบ ให้สร้างใหม่
		if updatedContact.ID == 0 || existingContactMap[updatedContact.ID].ID == 0 {
			// ตรวจสอบว่ามี contact อยู่แล้วหรือไม่
			if len(existingContacts) > 0 {
				// ถ้ามี contact อยู่แล้ว ให้อัปเดต contact แรก
				contact := existingContacts[0]
				contact.Name = updatedContact.Name
				contact.Bio = updatedContact.Bio

				// อัปเดต platforms
				if len(updatedContact.Platforms) > 0 {
					// ลบ platforms เดิม
					if err := db.Where("contact_id = ?", contact.ID).Delete(&Platform{}).Error; err != nil {
						return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete old platforms"})
					}

					// สร้าง platforms ใหม่
					for _, platform := range updatedContact.Platforms {
						newPlatform := Platform{
							PlatformID: platform.PlatformID,
							ContactID:  contact.ID,
							Title:      platform.Title,
							URL:        platform.URL,
						}
						if err := db.Create(&newPlatform).Error; err != nil {
							return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create platform"})
						}
					}
				}

				if err := db.Save(&contact).Error; err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update contact"})
				}
			} else {
				// ถ้าไม่มี contact อยู่เลย ให้สร้างใหม่
				newContact := Contact{
					Name:   updatedContact.Name,
					Bio:    updatedContact.Bio,
					UserID: uint(userID),
				}
				if err := db.Create(&newContact).Error; err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create contact"})
				}

				// สร้าง platforms สำหรับ contact ใหม่
				if len(updatedContact.Platforms) > 0 {
					for _, platform := range updatedContact.Platforms {
						newPlatform := Platform{
							PlatformID: platform.PlatformID,
							ContactID:  newContact.ID,
							Title:      platform.Title,
							URL:        platform.URL,
						}
						if err := db.Create(&newPlatform).Error; err != nil {
							return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create platform"})
						}
					}
				}
			}
		} else {
			// ถ้ามี contact_id และมีอยู่ในระบบ ให้อัปเดตข้อมูล
			contact := existingContactMap[updatedContact.ID]
			contact.Name = updatedContact.Name
			contact.Bio = updatedContact.Bio

			// อัปเดต platforms
			if len(updatedContact.Platforms) > 0 {
				// ลบ platforms เดิม
				if err := db.Where("contact_id = ?", contact.ID).Delete(&Platform{}).Error; err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete old platforms"})
				}

				// สร้าง platforms ใหม่
				for _, platform := range updatedContact.Platforms {
					newPlatform := Platform{
						PlatformID: platform.PlatformID,
						ContactID:  contact.ID,
						Title:      platform.Title,
						URL:        platform.URL,
					}
					if err := db.Create(&newPlatform).Error; err != nil {
						return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create platform"})
					}
				}
			}

			if err := db.Save(&contact).Error; err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update contact"})
			}
		}
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"message": "Contact and platforms updated successfully"})
}

func updateUserProfileAndContacts(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	var request struct {
		Contacts []Contact `json:"contacts"`
	}

	// Parse request body
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Debug log
	fmt.Println("Request received:", request)

	// ประกาศ contactIDs ไว้นอก transaction
	var contactIDs []uint

	// ดำเนินการด้วย transaction
	err := db.Transaction(func(tx *gorm.DB) error {
		// Find user by ID
		var user User
		if err := tx.First(&user, uint(userID)).Error; err != nil {
			return fiber.NewError(fiber.StatusNotFound, "User not found")
		}

		// วนลูปสร้างหรืออัปเดต contact
		for _, contact := range request.Contacts {
			// ตรวจสอบด้วย log
			fmt.Println("Processing contact:", contact.Name)

			// สำเนา platforms ไว้ก่อน เพราะใน contact ที่ส่งมามักมี platforms แนบมาด้วย
			contactPlatforms := contact.Platforms
			contact.Platforms = nil // ล้าง platforms ก่อนเพื่อไม่ให้ GORM พยายามจัดการ nested objects

			contact.UserID = uint(userID)

			var existingContact Contact
			// ตรวจสอบว่า contact นี้มีอยู่หรือไม่
			if err := tx.Where("user_id = ? AND name = ?", uint(userID), contact.Name).First(&existingContact).Error; err == nil {
				// ถ้ามี contact อยู่แล้ว ให้อัปเดตเฉพาะข้อมูลที่จำเป็น
				if err := tx.Model(&Contact{ID: existingContact.ID}).Updates(map[string]interface{}{
					"bio": contact.Bio,
				}).Error; err != nil {
					return fiber.NewError(fiber.StatusInternalServerError, "Failed to update contact")
				}

				fmt.Println("Updated existing contact:", existingContact.ID)

				// จัดการ platforms
				if len(contactPlatforms) > 0 {
					// ลบ platforms เดิมทั้งหมด
					if err := tx.Where("contact_id = ?", existingContact.ID).Delete(&Platform{}).Error; err != nil {
						return fiber.NewError(fiber.StatusInternalServerError, "Failed to delete existing platforms")
					}

					// สร้าง platforms ใหม่
					for i := range contactPlatforms {
						contactPlatforms[i].ID = 0 // รีเซ็ต ID ก่อนสร้าง
						contactPlatforms[i].ContactID = existingContact.ID
					}

					if err := tx.Create(&contactPlatforms).Error; err != nil {
						return fiber.NewError(fiber.StatusInternalServerError, "Failed to create platforms")
					}

					fmt.Println("Created", len(contactPlatforms), "new platforms for contact", existingContact.ID)
				}

				contactIDs = append(contactIDs, existingContact.ID)
			} else {
				// ถ้าไม่มี contact นี้ ให้สร้างใหม่โดยไม่มี platforms ก่อน
				if err := tx.Create(&contact).Error; err != nil {
					return fiber.NewError(fiber.StatusInternalServerError, "Failed to create contact")
				}

				fmt.Println("Created new contact:", contact.ID)

				// สร้าง platforms หลังจากสร้าง contact แล้ว
				if len(contactPlatforms) > 0 {
					for i := range contactPlatforms {
						contactPlatforms[i].ID = 0 // รีเซ็ต ID
						contactPlatforms[i].ContactID = contact.ID
					}

					if err := tx.Create(&contactPlatforms).Error; err != nil {
						return fiber.NewError(fiber.StatusInternalServerError, "Failed to create platforms")
					}

					fmt.Println("Created", len(contactPlatforms), "new platforms for new contact", contact.ID)
				}

				contactIDs = append(contactIDs, contact.ID)
			}
		}

		// Update form completion status
		user.FormOneCompleted = true
		if err := tx.Save(&user).Error; err != nil {
			return fiber.NewError(fiber.StatusInternalServerError, "Failed to update user form status")
		}

		return nil
	})

	// เช็ค error จาก transaction
	if err != nil {
		fmt.Println("Transaction error:", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	// ส่ง response พร้อม contactIDs
	return c.JSON(fiber.Map{
		"message": "Profile and contacts updated successfully",
	})
}

func deletePlatform(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	var request struct {
		PlatformNumber uint `json:"platform_number" validate:"required"`
	}

	// Parse request body
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// หา contact ตัวแรกของ user
	var contact Contact
	if err := db.Where("user_id = ?", uint(userID)).First(&contact).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "No contact found for this user",
		})
	}

	// ลบ Platform โดยใช้ platform_number (ID)
	if err := db.Where("contact_id = ? AND id = ?", contact.ID, request.PlatformNumber).Delete(&Platform{}).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to delete platform",
		})
	}

	// ส่งการตอบกลับ
	return c.JSON(fiber.Map{
		"message": "Platform deleted successfully",
	})
}

func updateContactComponentIDs(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	var request struct {
		ComponentIDs []uint `json:"component_ids" validate:"required"`
	}

	// Parse request body
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Validate request
	if err := validate.Struct(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   "Validation failed",
			"details": err.Error(),
		})
	}

	// หา contact ตัวแรกของ user
	var contact Contact
	if err := db.Where("user_id = ?", uint(userID)).First(&contact).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "No contact found for this user",
		})
	}

	// อัปเดต component_ids ในฐานข้อมูล
	contact.ComponentIDs = ComponentIDs(request.ComponentIDs)
	if err := db.Model(&contact).Update("component_ids", contact.ComponentIDs).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to update component IDs",
		})
	}

	// ดึงค่าที่อัปเดตมาแสดง
	if err := db.First(&contact, contact.ID).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch updated contact",
		})
	}

	// Update form completion status
	var user User
	if err := db.First(&user, uint(userID)).Error; err == nil {
		user.FormThreeCompleted = true
		db.Save(&user)
	}

	// ส่งค่าที่อัปเดตกลับไป
	return c.JSON(fiber.Map{
		"message":       "Component IDs updated successfully",
		"component_ids": contact.ComponentIDs,
	})
}

func login(db *gorm.DB, c *fiber.Ctx) error {
	var input User
	var user User

	// Parse the request body to get the input
	if err := c.BodyParser(&input); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request format"})
	}

	// Find user by email
	if err := db.Where("email = ?", input.Email).First(&user).Error; err != nil {
		// User not found
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
	}

	// Check if password matches
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
		// Incorrect password
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Invalid password"})
	}

	// Get user's contacts and platforms
	var contacts []Contact
	if err := db.Where("user_id = ?", user.ID).Find(&contacts).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch contacts"})
	}

	// Create JWT token
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["user_id"] = user.ID
	claims["exp"] = time.Now().Add(time.Hour * 72).Unix()

	// Add contact IDs to claims
	var contactIDs []uint
	for _, contact := range contacts {
		contactIDs = append(contactIDs, contact.ID)
	}
	claims["contact_ids"] = contactIDs

	// Add platform IDs to claims
	var platformIDs []uint
	for _, contact := range contacts {
		var platforms []Platform
		if err := db.Where("contact_id = ?", contact.ID).Find(&platforms).Error; err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch platforms"})
		}
		for _, platform := range platforms {
			platformIDs = append(platformIDs, platform.ID)
		}
	}
	claims["platform_ids"] = platformIDs

	// Sign the token
	t, err := token.SignedString(jwtSecretKey)
	if err != nil {
		// JWT creation error
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create token"})
	}

	// ไม่ต้อง set cookie แล้ว
	// c.Cookie(&fiber.Cookie{ ... })  <-- ลบออก

	// คืน token ใน response
	return c.JSON(fiber.Map{
		"message": "Login successful",
		"token":   t,
	})
}

func getUserByID(db *gorm.DB, c *fiber.Ctx) error {
	// Get JWT token from header or cookie
	tokenString := getJWTFromHeaderOrCookie(c)
	if tokenString == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "No JWT token found",
		})
	}

	// Parse and validate JWT token
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return jwtSecretKey, nil
	})

	if err != nil || !token.Valid {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid token",
		})
	}

	// Get user ID from token claims
	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	// Find user by ID
	var user User
	if err := db.Preload("Contacts.Platforms").First(&user, uint(userID)).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "User not found",
		})
	}

	// Return user data
	return c.JSON(fiber.Map{
		"user": user,
	})
}

type UpdateContactImageRequest struct {
	UserID       uint                  `form:"user_id" validate:"required"`
	ContactID    uint                  `form:"contact_id" validate:"required"`
	ImageProfile *multipart.FileHeader `form:"image_profile"`
	ImageCover   *multipart.FileHeader `form:"image_cover"`
}

func updateContactImage(db *gorm.DB, cld *cloudinary.Cloudinary, ctx context.Context, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	// Parse form files (ไม่ต้อง parse user_id/contact_id จาก form แล้ว)
	imageProfile, _ := c.FormFile("image_profile")
	imageCover, _ := c.FormFile("image_cover")

	// หา contact ตัวแรกของ user
	var contact Contact
	if err := db.Where("user_id = ?", uint(userID)).First(&contact).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "No contact found for this user",
		})
	}

	// Upload profile image if exists
	if imageProfile != nil {
		profileResp, err := cld.Upload.Upload(ctx, imageProfile, uploader.UploadParams{
			PublicID:       fmt.Sprintf("contact_profile_%d_%d", uint(userID), contact.ID),
			UniqueFilename: api.Bool(true),
			Overwrite:      api.Bool(true),
		})
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to upload profile image",
			})
		}
		db.Model(&contact).Update("image_profile", profileResp.SecureURL)
	}

	// Upload cover image if exists
	if imageCover != nil {
		coverResp, err := cld.Upload.Upload(ctx, imageCover, uploader.UploadParams{
			PublicID:       fmt.Sprintf("contact_cover_%d_%d", uint(userID), contact.ID),
			UniqueFilename: api.Bool(true),
			Overwrite:      api.Bool(true),
		})
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "Failed to upload cover image",
			})
		}
		db.Model(&contact).Update("image_cover", coverResp.SecureURL)
	}

	// Return success response with updated contact
	if err := db.First(&contact, contact.ID).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch updated contact",
		})
	}

	return c.JSON(fiber.Map{
		"message": "Contact image updated successfully",
		"contact": contact,
	})
}

func getUserByDomain(db *gorm.DB, c *fiber.Ctx) error {
	// รับค่าจาก URL path
	domain := c.Params("domain")

	// Debug ดูว่าค่า domain ได้อะไร
	fmt.Println("Domain:", domain)
	if domain == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Domain is required",
		})
	}

	// ค้นหาผู้ใช้จากฐานข้อมูล
	var user User
	if err := db.Preload("Contacts.Platforms").Where("domain = ?", domain).First(&user).Error; err != nil {
		fmt.Println("User not found for domain:", domain) // Debug
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "User not found",
		})
	}

	// ส่งข้อมูลผู้ใช้กลับไป
	return c.JSON(fiber.Map{
		"user": user,
	})
}

// ฟังก์ชันสำหรับอัปโหลด component ID
func uploadComponentID(db *gorm.DB, c *fiber.Ctx) error {
	var request struct {
		UserID      uint `json:"user_id" validate:"required"`
		ContactID   uint `json:"contact_id" validate:"required"`
		ComponentID uint `json:"component_id" validate:"required"`
	}

	// Parse request body
	if err := c.BodyParser(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid request body",
		})
	}

	// Validate request
	if err := validate.Struct(&request); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":   "Validation failed",
			"details": err.Error(),
		})
	}

	// ค้นหา Contact
	var contact Contact
	if err := db.Where("id = ? AND user_id = ?", request.ContactID, request.UserID).First(&contact).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "Contact not found or unauthorized",
		})
	}

	// อัปเดต component_id ในฐานข้อมูล
	contact.ComponentIDs = append(contact.ComponentIDs, request.ComponentID)
	if err := db.Model(&contact).Update("component_ids", contact.ComponentIDs).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to upload component ID",
		})
	}

	// ดึงค่าที่อัปเดตมาแสดง
	if err := db.First(&contact, request.ContactID).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch updated contact",
		})
	}

	// ส่งค่าที่อัปเดตกลับไป
	return c.JSON(fiber.Map{
		"message":       "Component ID uploaded successfully",
		"component_ids": contact.ComponentIDs,
	})
}

func fetchUserByID(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from URL parameters
	userID := c.Params("id")

	// Find user by ID
	var user User
	if err := db.Preload("Contacts.Platforms").First(&user, userID).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "User not found",
		})
	}

	// Return user data
	return c.JSON(fiber.Map{
		"user": user,
	})
}

// ฟังก์ชันใหม่: อัปโหลดรูปภาพและบันทึกไว้ใน server (local)
func updateContactImageLocal(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)

	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	// รับไฟล์จาก form
	imageProfile, _ := c.FormFile("image_profile")
	imageCover, _ := c.FormFile("image_cover")

	// หา contact ตัวแรกของ user
	var contact Contact
	if err := db.Where("user_id = ?", uint(userID)).First(&contact).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "No contact found for this user",
		})
	}

	// เตรียมโฟลเดอร์ uploads ถ้ายังไม่มี
	uploadDir := "./uploads"
	if _, err := os.Stat(uploadDir); os.IsNotExist(err) {
		err := os.MkdirAll(uploadDir, 0755)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create upload directory"})
		}
	}

	// Upload profile image if exists
	if imageProfile != nil {
		if !isValidFileExtension(imageProfile.Filename) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid file extension for profile image"})
		}
		if !isValidFileSize(imageProfile.Size) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Profile image file size too large"})
		}

		// แปลงภาพเป็น WebP
		webpData, err := convertToWebP(imageProfile, 80.0) // quality 80%
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to convert profile image to WebP: " + err.Error()})
		}

		// บันทึกไฟล์ WebP
		filename := fmt.Sprintf("profile_%d_%d.webp", uint(userID), contact.ID)
		savePath := fmt.Sprintf("%s/%s", uploadDir, filename)
		if err := os.WriteFile(savePath, webpData, 0644); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save profile image as WebP: " + err.Error()})
		}

		db.Model(&contact).Update("image_profile", "/uploads/"+filename)
	}

	// Upload cover image if exists
	if imageCover != nil {
		if !isValidFileExtension(imageCover.Filename) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid file extension for cover image"})
		}
		if !isValidFileSize(imageCover.Size) {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Cover image file size too large"})
		}

		// แปลงภาพเป็น WebP
		webpData, err := convertToWebP(imageCover, 80.0) // quality 80%
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to convert cover image to WebP: " + err.Error()})
		}

		// บันทึกไฟล์ WebP
		filename := fmt.Sprintf("cover_%d_%d.webp", uint(userID), contact.ID)
		savePath := fmt.Sprintf("%s/%s", uploadDir, filename)
		if err := os.WriteFile(savePath, webpData, 0644); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save cover image as WebP: " + err.Error()})
		}

		db.Model(&contact).Update("image_cover", "/uploads/"+filename)
	}

	// Update form completion status
	var user User
	if err := db.First(&user, uint(userID)).Error; err == nil {
		user.FormTwoCompleted = true
		db.Save(&user)
	}

	// Return success response with updated contact
	if err := db.First(&contact, contact.ID).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to fetch updated contact"})
	}

	return c.JSON(fiber.Map{
		"message": "Contact image updated successfully (local)",
		"contact": contact,
	})
}

func getUserStatus(db *gorm.DB, c *fiber.Ctx) error {
	// Get user ID from JWT token
	token := c.Locals("jwt").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)
	userID, ok := claims["user_id"].(float64)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "Invalid user ID in token",
		})
	}

	var user User
	if err := db.First(&user, uint(userID)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Database error"})
	}
	//
	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"form_one_completed":   user.FormOneCompleted,
		"form_two_completed":   user.FormTwoCompleted,
		"form_three_completed": user.FormThreeCompleted,
	})
}

// ฟังก์ชันใหม่: ดึง domain ของผู้ใช้ทุกคน
func getAllUserDomains(db *gorm.DB, c *fiber.Ctx) error {
	var users []User

	// ดึงข้อมูล domain ของผู้ใช้ทุกคน
	if err := db.Select("id, domain, email").Find(&users).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to fetch user domains",
		})
	}

	// สร้าง response ที่มีเฉพาะข้อมูลที่ต้องการ
	type UserDomainResponse struct {
		ID     uint   `json:"id"`
		Domain string `json:"domain"`
		Email  string `json:"email"`
	}

	var response []UserDomainResponse
	for _, user := range users {
		response = append(response, UserDomainResponse{
			ID:     user.ID,
			Domain: user.Domain,
			Email:  user.Email,
		})
	}

	return c.JSON(fiber.Map{
		"users": response,
		"count": len(response),
	})
}
