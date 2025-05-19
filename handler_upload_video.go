package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
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

	key := make([]byte, 32)
	_, err = rand.Read(key)
	fileKey := base64.RawURLEncoding.EncodeToString(key)
	fileName := fmt.Sprintf("%s.%s", fileKey, mediaType)

	upload := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(fileName),
		Body:        tmp,
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

	videoURL := fmt.Sprintf("https://%s.s3.amazonaws.com/%s", cfg.s3Bucket, fileName)
	vid.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(vid)

	respondWithJSON(w, http.StatusOK, vid)
}
