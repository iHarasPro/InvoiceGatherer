package main

import (
  "archive/zip"
  "context"
  "embed"
  "encoding/base64"
  "encoding/json"
  "fmt"
  "io"
  "log"
  "net/http"
  "os"
  "os/exec"
  "path/filepath"
  "runtime"
  "strconv"
  "strings"
  "time"

  "golang.org/x/oauth2"
  "golang.org/x/oauth2/google"
  "google.golang.org/api/gmail/v1"
  "google.golang.org/api/option"

  "fyne.io/fyne/v2"
  "fyne.io/fyne/v2/app"
  "fyne.io/fyne/v2/container"
  "fyne.io/fyne/v2/widget"
)

//go:embed credentials.json
var credentialsFile embed.FS

func main() {

  // Create the GUI
  mainApp := app.New()
  mainWindow := mainApp.NewWindow("Invoice Gatherer")

  // Get the Label Name
  label := widget.NewEntry()
  label.SetPlaceHolder("Label Name")

  // Get Start & End Dates
  wDateStr := widget.NewEntry()
  wDateEnd := widget.NewEntry()
  wDateStr.SetPlaceHolder("YYYY-MM-DD")
  wDateEnd.SetPlaceHolder("YYYY-MM-DD")

  // Label
  status := widget.NewLabel("Select the beginning and ending dates for invoices")

  // Start Button
  startBtn := widget.NewButton("Download", func() {

    // Label
    if label.Text == "" {
      status.SetText("Label field is required.")
      return
    }

    // Start Date
    dateStr, err := time.Parse("2006-01-02", wDateStr.Text)
    if err != nil {
      status.SetText("Invalid date format. Use 'YYYY-MM-DD'")
      return
    }

    // End Date
    dateEnd, err := time.Parse("2006-01-02", wDateEnd.Text)
    if err != nil {
      status.SetText("Invalid date format. Use 'YYYY-MM-DD'")
      return
    }

    // Update Status
    status.SetText("Downloading...")

    go func() {

      // Get Config
      config := getConfig()

      // Get Client
      client := getClient(config)

      // Create Gmail service in background
      srv, err := gmail.NewService(context.Background(), option.WithHTTPClient(client))
      if err != nil {
        log.Fatalf("Unable to create Gmail service: %v", err)
      }

      // Get the count of PDF files for the naming purposes
      files,err := filepath.Glob("Invoices/*.pdf")
      if err != nil {
        log.Fatalf("Unable to get the count of PDFs: %v", err)
      }
      count := len(files)

      // Download the Attachments
      pdfFiles, zipFiles := downloadAttachments(srv, label.Text, dateStr, dateEnd)

      // Update Status
      status.SetText("Extracting ZIP files...")

      // Extract ZIP files and Update the list of PDF files
      pdfFiles = append(pdfFiles, extractArchive(zipFiles)...)

      // Update Status
      status.SetText("Renaming...")

      // Rename PDFs
      for _, pdfFile := range pdfFiles {
        err = os.Rename(filepath.Join("Invoices", pdfFile), filepath.Join("Invoices", fmt.Sprintf("%s %s.pdf", strconv.Itoa(count), invoiceDetails(pdfFile))))
        count += 1
      }

      // Update Status
      status.SetText("Download Complete")

      return
    }()
  })

  // Set the Window Content
  content := container.NewVBox(
    widget.NewLabel("Label: ",), label,
    widget.NewLabel("Start Date: "), wDateStr,
    widget.NewLabel("End Date: "), wDateEnd,
    startBtn,
    status,
  )

  // Draw the Window
  mainWindow.SetContent(content)
  mainWindow.Resize(fyne.NewSize(480,360))
  mainWindow.ShowAndRun()
}


func getConfig() (*oauth2.Config) {

  // Read the embedded Credentials File
  credentials, err := credentialsFile.ReadFile("credentials.json")
  if err != nil {
    log.Fatalf("Failed to read the embedded credentials: %v", err)
  }

  // Get the Config from Credentials File
  config, err := google.ConfigFromJSON(credentials, gmail.GmailReadonlyScope)
  if err != nil {
    log.Fatalf("Unable to parse client credentials file: %v", err)
  }

  return config
}


func getClient(config *oauth2.Config) (*http.Client) {

  // Path to the Token file
  tokenPath := "InvoiceGathererToken.json"

  // Get the Token
  token, err := getTokenFromFile(tokenPath)
  if err != nil {
    token = getTokenFromWeb(config)
    saveToken(tokenPath, token)
  }

  return config.Client(context.Background(), token)
}


func getTokenFromFile(tokenPath string) (*oauth2.Token, error) {
  // Open the Token File
  tokenFile, err := os.Open(tokenPath)
  if err != nil {
    return nil, err
  }
  // Close the Token File
  defer tokenFile.Close()

  // Initialize and Assign Token
  token := &oauth2.Token{}
  err    = json.NewDecoder(tokenFile).Decode(token)

  return token, err
}


func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token) {

  // Channel to receive Auth Code
  authCodeChan := make(chan string)

  // Create HTTP Server
  server := &http.Server{Addr: ":8080"}

  // OAuth Callback Handler
  http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    if code == "" {
      http.Error(w, "Authorization code not found", http.StatusBadRequest)
      return
    }
    fmt.Fprintf(w, "Authorization successful! You can close this tab.")

    // Send the Code through the Channel
    authCodeChan <- code

    go func() {
      _ = server.Shutdown(context.Background())
    }()
  })

  go func() {
    if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
      log.Fatalf("Server error: %v", err)
    }
  }()

  // Generate the OAuth URL
  authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

  // Open the default browser
  var cmd *exec.Cmd
  switch runtime.GOOS {
    case "linux":
      cmd = exec.Command("xdg-open", authURL)
    case "darwin":
      cmd = exec.Command("open", authURL)
  }
  err := cmd.Start()
  if err != nil {
    log.Fatalf("Unable to open Web Browser: %v", err)
  }

  // Get the Code
  authCode := <- authCodeChan

  // Exchange the Auth Code for a Token
  token, err := config.Exchange(context.Background(), authCode)
  if err != nil {
    log.Fatalf("Unable to retrieve token: %v", err)
  }

  return token
}


func saveToken(tokenPath string, token *oauth2.Token) {
  // Create & Open Token file
  tokenFile, err := os.Create(tokenPath)
  if err != nil {
    log.Fatalf("Unable to save token file: %v", err)
  }
  defer tokenFile.Close()
  // Save the Token into Token File
  json.NewEncoder(tokenFile).Encode(token)
}


func downloadAttachments(service *gmail.Service, label string, dateStr time.Time, dateEnd time.Time) ([]string, []string) {
  // Return Values
  pdfFiles := make([]string, 0)
  zipFiles := make([]string, 0)

  // User
  user := "me"

  // Get all mails with the correct label that has attachments
  response, err := service.Users.Messages.List(user).Q(fmt.Sprintf("label:%s has:attachment after:%d/%d/%d before:%d/%d/%d", label, dateStr.Year(), dateStr.Month(), dateStr.Day(), dateEnd.Year(), dateEnd.Month(), dateEnd.Day())).Do()
  if err != nil {
    log.Fatalf("Unable to retrieve messages: %v", err)
  }

  // For each Message
  for _, msg := range response.Messages {
    // Get message
    message, err := service.Users.Messages.Get(user, msg.Id).Do()
    if err != nil {
      log.Printf("Unable to get message %s: %v", msg.Id, err)
    }

    // For each Payload Part
    for _, part := range message.Payload.Parts {
      // Do nothing if the file is not a PDF or a ZIP
      if part.Filename == "" || (strings.HasSuffix(part.Filename, ".pdf") == false && strings.HasSuffix(part.Filename, ".zip") == false) {
        continue
      }

      // Get the Attachment
      attachment, err := service.Users.Messages.Attachments.Get(user, msg.Id, part.Body.AttachmentId).Do()
      if err != nil {
        log.Printf("Failed to download attachment %s: %v", part.Filename, err)
        continue
      }

      // Decode Attachment
      data, err := base64.URLEncoding.DecodeString(attachment.Data)
      if err != nil {
        log.Printf("Failed to decode attachment %s: %v", part.Filename, err)
        continue
      }

      // Destination
      dest := filepath.Join("Invoices", part.Filename)

      // Save the File
      err = os.WriteFile(dest, data, 0644)
      if err != nil {
        log.Printf("Failed to save file %s: %v", part.Filename, err)
        continue
      }
      // Add filename to the return slice
      if strings.HasSuffix(part.Filename, ".pdf") {
        pdfFiles = append(pdfFiles, part.Filename)
      }
      if strings.HasSuffix(part.Filename, ".zip") {
        zipFiles = append(pdfFiles, part.Filename)
      }
    }
  }
  return pdfFiles, zipFiles
}


func extractArchive(zipFiles []string) ([]string) {
  // Return Value
  pdfFiles := make([]string, 0)

  // For each ZIP file
  for _,zipFile := range zipFiles {

    // Update the Path
    zipFile = filepath.Join("Invoices", zipFile)

    // Open the ZIP file
    zipReader, err := zip.OpenReader(zipFile)
    if err != nil {
      log.Printf("Unable to open ZIP file: %v", err)
      continue
    }
    // Close the ZIP file
    defer zipReader.Close()

    // For each File inside the ZIP file
    for _, file := range zipReader.File {
      // If the current file is a PDF
      if strings.HasSuffix(file.Name, ".pdf") {
        // Open the PDF
        src, err := file.Open()
        if err != nil {
          log.Printf("Unable to extract PDF %s: %v", file.Name, err)
          continue
        }
        // Close the PDF
        defer src.Close()

        // Open the Destination
        dest, err := os.Create(filepath.Join(filepath.Dir(zipFile), file.Name))
        if err != nil {
          log.Printf("Unable to create destination for PDF %s: %v", file.Name, err)
          continue
        }
        // Close the Destination
        defer dest.Close()

        // Copy the contents from src to dest
        _, err = io.Copy(dest, src)
        if err != nil {
          log.Printf("Unable to copy the contents of PDF %s: %v", file.Name, err)
          continue
        }

        // Update the PDF files
        pdfFiles = append(pdfFiles, file.Name)
      }
    }
    // Delete the ZIP file
    err = os.Remove(zipFile)
    if err != nil {
      log.Printf("Unable to remove the ZIP file %s: %v", zipFile, err)
    }
  }
  // Return value
  return pdfFiles
}


func invoiceDetails(path string) (string) {
  companyName := "?"
  invoiceDate := "?"
  invoiceNo   := "?"
  return companyName + " " + invoiceDate + " " + invoiceNo
}
