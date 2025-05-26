package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	const maxSize = 1 << 30
	http.MaxBytesReader(w, r.Body, maxSize)
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaTypeHeader := strings.SplitAfter(header.Filename, ".")[1]
	mediaType, _, err := mime.ParseMediaType(mediaTypeHeader)
	if mediaType != "mp4" && mediaType != "avi" {
		respondWithError(w, http.StatusBadRequest, "unsupported file type", err)
		return
	}

	tmp, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		//
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	io.Copy(tmp, file)

	tmp.Seek(0, io.SeekStart)
	processedPath, err := processVideoForFastStart(tmp.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed processing video", err)
		return
	}
	processed, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed processing video", err)
		return
	}
	defer os.Remove(processed.Name())
	defer processed.Close()

	prefix := ""
	ratio, err := getVideoAspectRatio(tmp.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed processing video", err)
		return
	}
	if ratio == "16/9" {
		prefix = "landscape"
	} else if ratio == "9/16" {
		prefix = "portrait"
	} else {
		prefix = "other"
	}

	key := make([]byte, 32)
	_, err = rand.Read(key)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "", err)
		return
	}
	fileKey := base64.RawURLEncoding.EncodeToString(key)
	fileName := fmt.Sprintf("%s/%s.%s", prefix, fileKey, mediaType)

	upload := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(fileName),
		Body:        processed,
		ContentType: aws.String("video/mp4"),
	}

	_, err = cfg.s3Client.PutObject(r.Context(), upload)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed to upload file to s3", err)
		return
	}

	vid, err := cfg.db.GetVideo(videoID)
	if err != nil {
		return
	}

	// videoURL := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", cfg.s3Bucket, fileName)
	// videoURL, err := generatePresignedURL(cfg.s3Client, cfg.s3Bucket, fileName, time.Hour*1)
	videoURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, fileName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to generate resource url", err)
		return
	}
	vid.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(vid)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed to store video url", err)
		return
	}

	vid, _ = cfg.dbVideoToSignedVideo(vid)
	respondWithJSON(w, http.StatusOK, vid)
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {

	presignClient := s3.NewPresignClient(s3Client)
	params := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	r, err := presignClient.PresignGetObject(context.TODO(), params, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}

	return r.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil || len(*video.VideoURL) == 0 {
		return video, nil
	}
	parts := strings.Split(*video.VideoURL, ",")
	if len(parts) != 2 {
		return video, fmt.Errorf("to many parts in vid url")
	}
	bucket := parts[0]
	key := parts[1]

	vURL, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Minute*5)
	if err != nil {
		return video, err
	}

	video.VideoURL = &vURL
	return video, nil
}

func getVideoAspectRatio(filePath string) (string, error) {
	type VideoStream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	type FFProbeOutput struct {
		Streams []VideoStream `json:"streams"`
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		fmt.Println("error:", err)
		return "", err
	}

	var result FFProbeOutput
	err = json.Unmarshal(out.Bytes(), &result)
	if err != nil {
		return "", err
	}

	width := result.Streams[0].Width
	height := result.Streams[0].Height
	ratio := getAspectRatio(width, height)

	return ratio, nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)
	err := cmd.Run()

	if err != nil {
		fmt.Errorf("error setting faststart flag", err)
		return "", err
	}

	return outputPath, nil
}

func getAspectRatio(width, height int) string {
	if height == 0 {
		return "other"
	}

	ratio := float64(width) / float64(height)
	epsilon := 0.01

	if abs(ratio-16.0/9.0) < epsilon {
		return "16/9"
	} else if abs(ratio-9.0/16.0) < epsilon {
		return "9/16"
	}
	return "other"
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
