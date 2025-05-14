package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20

	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Exceeded maximum size", err)
		return
	}

	// mediaType := r.Header.Get("Content-Type")
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()
	mediaTypeHeader := strings.SplitAfter(header.Filename, ".")[1]
	mediaType, _, err := mime.ParseMediaType(mediaTypeHeader)
	fmt.Println(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "failed to parse file type", err)
		return
	}
	if mediaType != "png" && mediaType != "jpg" {
		respondWithError(w, http.StatusBadRequest, "unsupported file type", err)
		return
	}
	thumbFileName := fmt.Sprintf("/%s.%s", videoIDString, mediaType)
	thumbFilePath := filepath.Join(cfg.assetsRoot, thumbFileName)
	thumbFile, err := os.Create(thumbFilePath)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to store file", err)
		return
	}
	defer thumbFile.Close()

	_, err = io.Copy(thumbFile, file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "unable to write file", err)
		return
	}

	videoData, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	} else if videoData.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "", nil)
		return
	}

	thumbnailPath := fmt.Sprintf("http://localhost:8091/assets/%s", thumbFileName)
	videoData.ThumbnailURL = &thumbnailPath

	err = cfg.db.UpdateVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "something went wrong", err)
	}

	respondWithJSON(w, http.StatusOK, videoData)
}
