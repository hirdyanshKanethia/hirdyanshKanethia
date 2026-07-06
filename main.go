package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	// --- External Dependencies ---
	"github.com/fatih/color"
	"github.com/wader/ansisvg/ansitosvg"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[mK]|\x1b\]8;;.*?\x1b\\`)

type GitHubUser struct {
	Login       string `json:"login"`
	AvatarURL   string `json:"avatar_url"`
	Followers   int    `json:"followers"`
	PublicRepos int    `json:"public_repos"`
	CreatedAt   string `json:"created_at"`
}

type RepoStats struct {
	TotalStars int
}

func main() {
	// Force color output even in non-TTY environments like GitHub Actions
	color.NoColor = false

	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <github_username>\n", os.Args[0])
		os.Exit(1)
	}
	username := os.Args[1]

	// Configuration
	dob := "2005-12-18"

	user, err := fetchGitHubInfo(username)
	if err != nil {
		log.Fatalf("Failed to fetch user info: %v", err)
	}

	avatarImage, err := fetchAvatar(user.AvatarURL)
	if err != nil {
		log.Fatalf("Failed to fetch avatar: %v", err)
	}

	contribLines, contribCount, err := fetchContributions(username)
	if err != nil {
		log.Fatalf("Failed to fetch contributions: %v", err)
	}

	repoStats, err := fetchRepoStats(username)
	if err != nil {
		log.Printf("Failed to fetch repo stats: %v", err)
		repoStats = &RepoStats{} // empty fallback
	}

	token := os.Getenv("GITHUB_TOKEN")
	contributedTo := "??"
	allTimeCommits := "??"
	if token != "" {
		cont, commits, err := fetchAdvancedStats(username, token, user.CreatedAt)
		if err == nil {
			// Add user.PublicRepos to include personal repos
			contributedTo = fmt.Sprintf("%d", cont + user.PublicRepos)
			allTimeCommits = fmt.Sprintf("%d", commits)
		} else {
			log.Printf("Warning: Failed to fetch advanced stats via GraphQL: %v", err)
		}
	}

	art := ConvertHalfBlock(avatarImage, 48)
	printLayout(art, user, repoStats, contribLines, contribCount, dob, avatarImage, contributedTo, allTimeCommits)
}

func fetchAdvancedStats(username, token, createdAtStr string) (int, int, error) {
	// Parse user creation year
	createdTime, err := time.Parse(time.RFC3339, createdAtStr)
	if err != nil {
		return 0, 0, err
	}
	startYear := createdTime.Year()
	currentYear := time.Now().Year()

	// Build dynamic GraphQL query with aliases for every year
	var queryBuilder strings.Builder
	queryBuilder.WriteString(fmt.Sprintf(`query { user(login: "%s") { `, username))
	queryBuilder.WriteString(`repositoriesContributedTo(contributionTypes: [COMMIT, ISSUE, PULL_REQUEST, REPOSITORY]) { totalCount } `)

	for y := startYear; y <= currentYear; y++ {
		queryBuilder.WriteString(fmt.Sprintf(`y%d: contributionsCollection(from: "%d-01-01T00:00:00Z", to: "%d-12-31T23:59:59Z") { contributionCalendar { totalContributions } } `, y, y, y))
	}
	queryBuilder.WriteString(`} }`)

	reqBody, _ := json.Marshal(map[string]string{"query": queryBuilder.String()})

	req, err := http.NewRequest("POST", "https://api.github.com/graphql", strings.NewReader(string(reqBody)))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("graphql request failed: %s", resp.Status)
	}

	var result struct {
		Data struct {
			User map[string]interface{} `json:"user"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, 0, err
	}

	userMap := result.Data.User
	if userMap == nil {
		return 0, 0, fmt.Errorf("user not found in graphql response")
	}

	contributedTo := 0
	if rc, ok := userMap["repositoriesContributedTo"].(map[string]interface{}); ok {
		if tc, ok := rc["totalCount"].(float64); ok {
			contributedTo = int(tc)
		}
	}

	totalCommits := 0
	for y := startYear; y <= currentYear; y++ {
		key := fmt.Sprintf("y%d", y)
		if cc, ok := userMap[key].(map[string]interface{}); ok {
			if cal, ok := cc["contributionCalendar"].(map[string]interface{}); ok {
				if tc, ok := cal["totalContributions"].(float64); ok {
					totalCommits += int(tc)
				}
			}
		}
	}

	return contributedTo, totalCommits, nil
}

type Section struct {
	Title string
	Lines []string
}

// ConvertHalfBlock converts an image into a true-color ANSI half-block string.
func ConvertHalfBlock(img image.Image, width int) string {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	height := int(float64(width) * float64(srcH) / float64(srcW))
	if height%2 != 0 {
		height++
	}

	var sb strings.Builder
	for y := 0; y < height; y += 2 {
		for x := 0; x < width; x++ {
			srcX := bounds.Min.X + (x * srcW / width)
			srcYTop := bounds.Min.Y + (y * srcH / height)
			srcYBot := bounds.Min.Y + ((y + 1) * srcH / height)

			rTop, gTop, bTop, _ := img.At(srcX, srcYTop).RGBA()
			rBot, gBot, bBot, _ := img.At(srcX, srcYBot).RGBA()

			r1, g1, b1 := rTop>>8, gTop>>8, bTop>>8
			r2, g2, b2 := rBot>>8, gBot>>8, bBot>>8

			sb.WriteString(fmt.Sprintf("\x1b[48;2;%d;%d;%dm\x1b[38;2;%d;%d;%dm▄", r1, g1, b1, r2, g2, b2))
		}
		sb.WriteString("\x1b[0m\n")
	}
	return sb.String()
}

func fetchContributions(username string) ([]string, string, error) {
	resp, err := http.Get("https://github.com/users/" + username + "/contributions")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	totalCount := "0"
	reCount := regexp.MustCompile(`(?s)<h2[^>]*>\s*([\d,]+)\s*contributions`)
	if mCount := reCount.FindStringSubmatch(string(body)); len(mCount) > 1 {
		totalCount = mCount[1]
	}

	re := regexp.MustCompile(`id="contribution-day-component-(\d+)-(\d+)"[^>]*data-level="([0-4])"`)
	matches := re.FindAllStringSubmatch(string(body), -1)

	if len(matches) == 0 {
		return nil, totalCount, fmt.Errorf("no contributions found")
	}

	var grid [7][60]string
	maxCol := 0

	for _, m := range matches {
		rowIdx, colIdx := 0, 0
		fmt.Sscanf(m[1], "%d", &rowIdx)
		fmt.Sscanf(m[2], "%d", &colIdx)
		if colIdx > maxCol {
			maxCol = colIdx
		}
		if rowIdx >= 0 && rowIdx < 7 && colIdx >= 0 && colIdx < 60 {
			grid[rowIdx][colIdx] = m[3]
		}
	}

	c0 := color.New(color.FgHiBlack).SprintFunc()
	c1 := color.New(color.FgHiGreen).SprintFunc()
	c2 := color.New(color.FgGreen).SprintFunc()
	c3 := color.New(color.FgGreen, color.Bold).SprintFunc()
	c4 := color.New(color.FgHiYellow).SprintFunc()

	var lines []string
	for r := 0; r < 7; r++ {
		var lineStr string
		for c := 0; c <= maxCol; c++ {
			level := grid[r][c]
			switch level {
			case "0":
				lineStr += c0("■")
			case "1":
				lineStr += c1("■")
			case "2":
				lineStr += c2("■")
			case "3":
				lineStr += c3("■")
			case "4":
				lineStr += c4("■")
			default:
				lineStr += " "
			}
		}
		lines = append(lines, lineStr)
	}

	return lines, totalCount, nil
}

func fetchGitHubInfo(username string) (*GitHubUser, error) {
	url := "https://api.github.com/users/" + username
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API request failed with status: %s", resp.Status)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func fetchRepoStats(username string) (*RepoStats, error) {
	url := fmt.Sprintf("https://api.github.com/users/%s/repos?per_page=100", username)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repos API request failed: %s", resp.Status)
	}

	var repos []struct {
		StargazersCount int `json:"stargazers_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, err
	}

	stats := &RepoStats{}
	for _, repo := range repos {
		stats.TotalStars += repo.StargazersCount
	}

	return stats, nil
}

func fetchAvatar(url string) (image.Image, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download avatar: %s", resp.Status)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %v", err)
	}
	return img, nil
}

func getUptime(dobStr string) string {
	dob, err := time.Parse("2006-01-02", dobStr)
	if err != nil {
		return "Unknown"
	}
	now := time.Now()
	years := now.Year() - dob.Year()
	months := int(now.Month()) - int(dob.Month())
	days := now.Day() - dob.Day()

	if days < 0 {
		months--
		// get days in previous month
		t := time.Date(now.Year(), now.Month(), 0, 0, 0, 0, 0, time.UTC)
		days += t.Day()
	}
	if months < 0 {
		years--
		months += 12
	}
	
	if months == 0 && days == 0 {
		return fmt.Sprintf("%d years", years)
	} else if months == 0 {
		return fmt.Sprintf("%d years, %d days", years, days)
	} else if days == 0 {
		return fmt.Sprintf("%d years, %d months", years, months)
	}
	return fmt.Sprintf("%d years, %d months, %d days", years, months, days)
}

func getDominantColor(img image.Image) (uint8, uint8, uint8) {
	bounds := img.Bounds()
	counts := make(map[uint16]int)
	sums := make(map[uint16][3]uint64)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 32768 {
				continue
			}
			r8, g8, b8 := r>>8, g>>8, b>>8
			
			// Quantize to 5 bits to form buckets
			qR, qG, qB := r8>>3, g8>>3, b8>>3
			key := uint16(qR<<10 | qG<<5 | qB)

			counts[key]++
			s := sums[key]
			s[0] += uint64(r8)
			s[1] += uint64(g8)
			s[2] += uint64(b8)
			sums[key] = s
		}
	}

	var bestKey uint16
	var maxScore float64

	for k, v := range counts {
		r := float64(k>>10) * 8.0
		g := float64((k>>5)&31) * 8.0
		b := float64(k&31) * 8.0
		
		maxC := math.Max(r, math.Max(g, b))
		minC := math.Min(r, math.Min(g, b))
		
		// Brightness (0.0 to 1.0)
		brightness := maxC / 255.0
		
		// Saturation (0.0 to 1.0)
		var saturation float64
		if maxC > 0 {
			saturation = (maxC - minC) / maxC
		}
		
		// Penalize very dark colors
		if brightness < 0.2 {
			continue
		}
		
		// Penalize very white/gray colors
		if saturation < 0.15 {
			continue
		}

		// Score heavily favors saturation and brightness alongside frequency
		score := float64(v) * (saturation * saturation) * brightness
		
		if score > maxScore {
			maxScore = score
			bestKey = k
		}
	}

	// Fallback if no vibrant color is found
	if maxScore == 0 {
		var maxCount int
		for k, v := range counts {
			r, g, b := int(k>>10), int((k>>5)&31), int(k&31)
			if r+g+b > 10 { // Ignore pitch black
				if v > maxCount {
					maxCount = v
					bestKey = k
				}
			}
		}
		if maxCount == 0 {
			return 255, 255, 255 // Default to white
		}
	}

	s := sums[bestKey]
	count := uint64(counts[bestKey])
	finalR, finalG, finalB := uint8(s[0]/count), uint8(s[1]/count), uint8(s[2]/count)
	
	// Ensure the color is readable on dark terminal backgrounds
	brightnessScore := int(finalR) + int(finalG) + int(finalB)
	if brightnessScore < 200 {
		factor := 200.0 / float64(brightnessScore)
		fr := math.Min(255, float64(finalR)*factor)
		fg := math.Min(255, float64(finalG)*factor)
		fb := math.Min(255, float64(finalB)*factor)
		return uint8(fr), uint8(fg), uint8(fb)
	}

	return finalR, finalG, finalB
}

func printLayout(art string, user *GitHubUser, repoStats *RepoStats, contribLines []string, contribCount string, dob string, avatarImage image.Image, contributedTo string, allTimeCommits string) {
	// Extract dominant color from avatar
	r, g, b := getDominantColor(avatarImage)
	themeColor := color.RGB(int(r), int(g), int(b)).SprintFunc()
	
	white := color.New(color.FgWhite).SprintFunc()
	gray := color.RGB(100, 100, 100).SprintFunc()
	themeGreen := color.RGB(63, 185, 80).SprintFunc()
	themeRed := color.RGB(248, 81, 73).SprintFunc()
	// Helper for dot-padded lines
	totalWidth := 65 // Inner width of the text

	dotLine := func(labelName, valueStr string) string {
		label := gray(". ") + themeColor(labelName) + gray(":")
		lLen := 2 + utf8.RuneCountInString(labelName) + 1
		vLen := utf8.RuneCountInString(stripAnsi(valueStr))

		dotsNeeded := totalWidth - lLen - vLen - 1
		if dotsNeeded < 1 {
			dotsNeeded = 1
		}
		dots := gray(strings.Repeat(".", dotsNeeded))
		return fmt.Sprintf("%s %s %s", label, dots, valueStr)
	}

	var sections []Section

	// Header / Title line (not in a box)
	var titleLines []string
	titleLines = append(titleLines, white(user.Login)+gray("@")+white("github ")+gray(strings.Repeat("-", totalWidth-14)))
	sections = append(sections, Section{Title: "", Lines: titleLines})

	// 1. Personal Section
	var personal []string
	personal = append(personal, dotLine("Email", white("hirdyanshkanethia.18@gmail.com")))
	personal = append(personal, dotLine("Education", white("B.Tech, Computer Science and Engineering, 2027")))
	personal = append(personal, dotLine("Institute", white("NIT Srinagar")))
	personal = append(personal, dotLine("Uptime", white(getUptime(dob))))
	sections = append(sections, Section{Title: "Personal Info", Lines: personal})

	// 2. Coding Profiles
	var profiles []string
	profiles = append(profiles, dotLine("GitHub", white("hirdyanshKanethia")))
	profiles = append(profiles, dotLine("LeetCode", white("HIrdyansh_k")))
	profiles = append(profiles, dotLine("Codeforces", white("hirdyanshkanethia.18")))
	sections = append(sections, Section{Title: "Coding Profiles", Lines: profiles})

	// 3. Socials Section
	var socials []string
	socials = append(socials, dotLine("Twitter", white("Hi_rdyanshK")))
	socials = append(socials, dotLine("LinkedIn", white("hirdyansh-k")))
	socials = append(socials, dotLine("Instagram", white("hirdyansh_k")))
	sections = append(sections, Section{Title: "Socials", Lines: socials})

	// 4. GitHub Stats Section
	var ghStats []string
	
	// Repos | Stars
	reposVal := white(fmt.Sprintf("%d ", user.PublicRepos)) + gray("{") + themeColor("Contributed") + gray(": ") + white(contributedTo) + gray("}")
	left1 := gray(". ") + themeColor("Repos") + gray(":")
	left1Len := 2 + 5 + 1
	left1ValLen := utf8.RuneCountInString(stripAnsi(reposVal))
	dots1Needed := 32 - left1Len - left1ValLen - 1
	if dots1Needed < 1 { dots1Needed = 1 }
	left1Str := left1 + " " + gray(strings.Repeat(".", dots1Needed)) + " " + reposVal

	right1 := themeColor("Stars") + gray(":")
	right1Val := white(fmt.Sprintf("%d", repoStats.TotalStars))
	right1Len := 5 + 1
	right1ValLen := utf8.RuneCountInString(stripAnsi(right1Val))
	dotsR1Needed := totalWidth - 32 - 3 - right1Len - right1ValLen - 1 // 3 for " | "
	if dotsR1Needed < 1 { dotsR1Needed = 1 }
	right1Str := right1 + " " + gray(strings.Repeat(".", dotsR1Needed)) + " " + right1Val

	ghStats = append(ghStats, left1Str+" "+gray("|")+" "+right1Str)

	// Commits | Followers
	left2 := gray(". ") + themeColor("Commits") + gray(":")
	left2Val := white(allTimeCommits)
	left2Len := 2 + 7 + 1
	left2ValLen := utf8.RuneCountInString(stripAnsi(left2Val))
	dots2Needed := 32 - left2Len - left2ValLen - 1
	if dots2Needed < 1 { dots2Needed = 1 }
	left2Str := left2 + " " + gray(strings.Repeat(".", dots2Needed)) + " " + left2Val

	right2 := themeColor("Followers") + gray(":")
	right2Val := white(fmt.Sprintf("%d", user.Followers))
	right2Len := 9 + 1
	right2ValLen := utf8.RuneCountInString(stripAnsi(right2Val))
	dotsR2Needed := totalWidth - 32 - 3 - right2Len - right2ValLen - 1
	if dotsR2Needed < 1 { dotsR2Needed = 1 }
	right2Str := right2 + " " + gray(strings.Repeat(".", dotsR2Needed)) + " " + right2Val

	ghStats = append(ghStats, left2Str+" "+gray("|")+" "+right2Str)

	// Lines of Code
	locVal := white("3,944,170") + gray(" (") + themeGreen("4,586,267++") + gray(", ") + themeRed("642,097--") + gray(")")
	ghStats = append(ghStats, dotLine("Lines of Code on GitHub", locVal))
	
	sections = append(sections, Section{Title: "GitHub Stats", Lines: ghStats})

	// Box rendering
	var infoLines []string
	infoWidth := totalWidth + 4 // Fixed width for info boxes

	for _, sec := range sections {
		if sec.Title == "" {
			for _, line := range sec.Lines {
				infoLines = append(infoLines, fmt.Sprintf("     %s", line))
			}
			continue
		}
		
		// Top border with title
		titleLen := utf8.RuneCountInString(sec.Title)
		dashCount := infoWidth - titleLen - 4
		leftDash := dashCount / 2
		rightDash := dashCount - leftDash
		
		infoLines = append(infoLines, fmt.Sprintf("   %s%s %s %s%s",
			themeColor("┌"),
			themeColor(strings.Repeat("─", leftDash)),
			themeColor(sec.Title),
			themeColor(strings.Repeat("─", rightDash)),
			themeColor("┐"),
		))

		for _, line := range sec.Lines {
			infoLines = append(infoLines, fmt.Sprintf("     %s", line))
		}

		infoLines = append(infoLines, fmt.Sprintf("   %s", themeColor("└"+strings.Repeat("─", infoWidth-2)+"┘")))
	}

	infoLines = append(infoLines, "   ")
	
	// Contributions Block
	title := fmt.Sprintf("Contributions (%s Last Year)", contribCount)
	titleLen := utf8.RuneCountInString(title)
	dashCount := infoWidth - titleLen - 4
	leftDash := dashCount / 2
	rightDash := dashCount - leftDash
	
	infoLines = append(infoLines, fmt.Sprintf("   %s%s %s %s%s",
		themeColor("┌"),
		themeColor(strings.Repeat("─", leftDash)),
		themeColor(title),
		themeColor(strings.Repeat("─", rightDash)),
		themeColor("┐"),
	))
	
	for _, cl := range contribLines {
		infoLines = append(infoLines, "     "+cl)
	}
	infoLines = append(infoLines, fmt.Sprintf("   %s", themeColor("└"+strings.Repeat("─", infoWidth-2)+"┘")))

	// Combine Art and Info side by side
	artLines := strings.Split(art, "\n")
	maxWidth := 0
	for _, line := range artLines {
		visualWidth := utf8.RuneCountInString(stripAnsi(line))
		if visualWidth > maxWidth {
			maxWidth = visualWidth
		}
	}

	gutter := "   "
	maxLines := len(artLines)
	if len(infoLines) > maxLines {
		maxLines = len(infoLines)
	}
	
	artOffset := 0
	infoOffset := 0
	if len(infoLines) > len(artLines) {
		artOffset = (len(infoLines) - len(artLines)) / 2
	} else if len(artLines) > len(infoLines) {
		infoOffset = (len(artLines) - len(infoLines)) / 2
	}

	var buf bytes.Buffer

	buf.WriteString("\n")
	for i := 0; i < maxLines; i++ {
		artLine := ""
		if i >= artOffset && i-artOffset < len(artLines) {
			artLine = artLines[i-artOffset]
		}
		infoLine := ""
		if i >= infoOffset && i-infoOffset < len(infoLines) {
			infoLine = infoLines[i-infoOffset]
		}

		visualWidth := utf8.RuneCountInString(stripAnsi(artLine))
		padding := strings.Repeat(" ", maxWidth-visualWidth)

		buf.WriteString(fmt.Sprintf("%s%s%s%s\n", artLine, padding, gutter, infoLine))
	}
	buf.WriteString("\n")

	// Print to terminal
	fmt.Print(buf.String())

	// Write to SVG
	svgFile, err := os.Create("github-stats.svg")
	if err != nil {
		log.Printf("Failed to create SVG file: %v", err)
		return
	}
	defer svgFile.Close()

	opts := ansitosvg.DefaultOptions
	opts.FontName = "'FiraCode Nerd Font', 'Hack Nerd Font', 'JetBrainsMono Nerd Font', 'MesloLGS NF', monospace"
	opts.FontSize = 20
	opts.Transparent = true

	if err := ansitosvg.Convert(strings.NewReader(buf.String()), svgFile, opts); err != nil {
		log.Printf("Failed to convert to SVG: %v", err)
	} else {
		fmt.Println("Successfully generated github-stats.svg!")
	}
}

func stripAnsi(str string) string {
	return ansiRegex.ReplaceAllString(str, "")
}
