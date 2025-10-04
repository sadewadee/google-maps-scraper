package gmaps

import (
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

type Image struct {
	Title string `json:"title"`
	Image string `json:"image"`
}

type LinkSource struct {
	Link   string `json:"link"`
	Source string `json:"source"`
}

type Owner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Link string `json:"link"`
}

type Address struct {
	Borough    string `json:"borough"`
	Street     string `json:"street"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	State      string `json:"state"`
	Country    string `json:"country"`
}

type Option struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type About struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Options []Option `json:"options"`
}

type Review struct {
	Name           string
	ProfilePicture string
	Rating         int
	Description    string
	Images         []string
	When           string
}

type Entry struct {
	ID         string              `json:"input_id"`
	Link       string              `json:"link"`
	Cid        string              `json:"cid"`
	Title      string              `json:"title"`
	Categories []string            `json:"categories"`
	Category   string              `json:"category"`
	Address    string              `json:"address"`
	OpenHours  map[string][]string `json:"open_hours"`
	// PopularTImes is a map with keys the days of the week
	// and value is a map with key the hour and value the traffic in that time
	PopularTimes        map[string]map[int]int `json:"popular_times"`
	WebSite             string                 `json:"web_site"`
	Phone               string                 `json:"phone"`
	PlusCode            string                 `json:"plus_code"`
	ReviewCount         int                    `json:"review_count"`
	ReviewRating        float64                `json:"review_rating"`
	ReviewsPerRating    map[int]int            `json:"reviews_per_rating"`
	Latitude            float64                `json:"latitude"`
	Longtitude          float64                `json:"longtitude"`
	Status              string                 `json:"status"`
	Description         string                 `json:"description"`
	ReviewsLink         string                 `json:"reviews_link"`
	Thumbnail           string                 `json:"thumbnail"`
	Timezone            string                 `json:"timezone"`
	PriceRange          string                 `json:"price_range"`
	DataID              string                 `json:"data_id"`
	Images              []Image                `json:"images"`
	Reservations        []LinkSource           `json:"reservations"`
	OrderOnline         []LinkSource           `json:"order_online"`
	Menu                LinkSource             `json:"menu"`
	Owner               Owner                  `json:"owner"`
	CompleteAddress     Address                `json:"complete_address"`
	About               []About                `json:"about"`
	UserReviews         []Review               `json:"user_reviews"`
	UserReviewsExtended []Review               `json:"user_reviews_extended"`
	Emails              []string               `json:"emails"`

	Facebook  string `json:"facebook,omitempty"`
	Instagram string `json:"instagram,omitempty"`
	LinkedIn  string `json:"linkedin,omitempty"`
	WhatsApp  string `json:"whatsapp,omitempty"`

	// Derived and enrichment fields for sample parity
	PlaceID               string   `json:"place_id,omitempty"`
	Kgmid                 string   `json:"kgmid,omitempty"`
	GoogleMapsURL         string   `json:"google_maps_url,omitempty"`
	GoogleKnowledgeURL    string   `json:"google_knowledge_url,omitempty"`
	ReviewURL             string   `json:"review_url,omitempty"`
	Domain                string   `json:"domain,omitempty"`
	Phones                []string `json:"phones,omitempty"`
	Claimed               string   `json:"claimed,omitempty"` // "YES"/"NO"
	FeaturedImage         string   `json:"featured_image,omitempty"`
	OpeningHoursFormatted string   `json:"opening_hours,omitempty"`

	Meta struct {
		Title       string `json:"title,omitempty"`
		Description string `json:"description,omitempty"`
	} `json:"meta,omitempty"`

	TrackingIDs struct {
		Google struct {
			UA  string `json:"ua,omitempty"`
			GA4 string `json:"ga4,omitempty"`
		} `json:"google,omitempty"`
	} `json:"tracking_ids,omitempty"`

	FacebookLinks  []string `json:"facebook_links,omitempty"`
	InstagramLinks []string `json:"instagram_links,omitempty"`
	LinkedInLinks  []string `json:"linkedin_links,omitempty"`
	PinterestLinks []string `json:"pinterest_links,omitempty"`
	TiktokLinks    []string `json:"tiktok_links,omitempty"`
	TwitterLinks   []string `json:"twitter_links,omitempty"`
	YelpLinks      []string `json:"yelp_links,omitempty"`
	YoutubeLinks   []string `json:"youtube_links,omitempty"`
}

func (e *Entry) haversineDistance(lat, lon float64) float64 {
	const R = 6371e3 // earth radius in meters

	clat := lat * math.Pi / 180
	clon := lon * math.Pi / 180

	elat := e.Latitude * math.Pi / 180
	elon := e.Longtitude * math.Pi / 180

	dlat := elat - clat
	dlon := elon - clon

	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(clat)*math.Cos(elat)*
			math.Sin(dlon/2)*math.Sin(dlon/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return R * c
}

func (e *Entry) isWithinRadius(lat, lon, radius float64) bool {
	distance := e.haversineDistance(lat, lon)

	return distance <= radius
}

func (e *Entry) IsWebsiteValidForEmail() bool {
	s := strings.ToLower(strings.TrimSpace(e.WebSite))
	if s == "" {
		return false
	}

	// Block common social/video platforms where email enrichment is not applicable
	block := []string{
		"facebook.com",
		"instagram.com",
		"twitter.com",
		"x.com",
		"tiktok.com",
		"youtube.com",
		"youtu.be",
	}

	for _, b := range block {
		if strings.Contains(s, b) {
			return false
		}
	}

	return true
}

func (e *Entry) Validate() error {
	if e.Title == "" {
		return fmt.Errorf("title is empty")
	}

	if e.Category == "" {
		return fmt.Errorf("category is empty")
	}

	return nil
}

func (e *Entry) CsvHeaders() []string {
	return []string{
		"input_id",
		"link",
		"title",
		"category",
		"address",
		"open_hours",
		"popular_times",
		"website",
		"phone",
		"plus_code",
		"review_count",
		"review_rating",
		"reviews_per_rating",
		"latitude",
		"longitude",
		"cid",
		"status",
		"descriptions",
		"reviews_link",
		"thumbnail",
		"timezone",
		"price_range",
		"data_id",
		"images",
		"reservations",
		"order_online",
		"menu",
		"owner",
		"complete_address",
		"about",
		"user_reviews",
		"user_reviews_extended",
		"emails",
		"facebook",
		"instagram",
		"linkedin",
		"whatsapp",
	}
}

func (e *Entry) CsvRow() []string {
	return []string{
		e.ID,
		e.Link,
		e.Title,
		e.Category,
		e.Address,
		stringify(e.OpenHours),
		stringify(e.PopularTimes),
		e.WebSite,
		e.Phone,
		e.PlusCode,
		stringify(e.ReviewCount),
		stringify(e.ReviewRating),
		stringify(e.ReviewsPerRating),
		stringify(e.Latitude),
		stringify(e.Longtitude),
		e.Cid,
		e.Status,
		e.Description,
		e.ReviewsLink,
		e.Thumbnail,
		e.Timezone,
		e.PriceRange,
		e.DataID,
		stringify(e.Images),
		stringify(e.Reservations),
		stringify(e.OrderOnline),
		stringify(e.Menu),
		stringify(e.Owner),
		stringify(e.CompleteAddress),
		stringify(e.About),
		stringify(e.UserReviews),
		stringify(e.UserReviewsExtended),
		stringSliceToString(e.Emails),
		e.Facebook,
		e.Instagram,
		e.LinkedIn,
		e.WhatsApp,
	}
}

func (e *Entry) AddExtraReviews(pages [][]byte) {
	if len(pages) == 0 {
		return
	}

	for _, page := range pages {
		reviews := extractReviews(page)
		if len(reviews) > 0 {
			e.UserReviewsExtended = append(e.UserReviewsExtended, reviews...)
		}
	}
}

func extractReviews(data []byte) []Review {
	if len(data) >= 4 && string(data[0:4]) == `)]}'` {
		data = data[4:] // Skip security prefix
	}

	var jd []any
	if err := json.Unmarshal(data, &jd); err != nil {
		fmt.Printf("Error unmarshalling JSON: %v\n", err)
		return nil
	}

	reviewsI := getNthElementAndCast[[]any](jd, 2)

	return parseReviews(reviewsI)
}

//nolint:gomnd // it's ok, I need the indexes
func EntryFromJSON(raw []byte, reviewCountOnly ...bool) (entry Entry, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from panic: %v stack: %s", r, debug.Stack())

			return
		}
	}()

	onlyReviewCount := false

	if len(reviewCountOnly) == 1 && reviewCountOnly[0] {
		onlyReviewCount = true
	}

	var jd []any
	if err := json.Unmarshal(raw, &jd); err != nil {
		return entry, err
	}

	if len(jd) < 7 {
		return entry, fmt.Errorf("invalid json")
	}

	darray, ok := jd[6].([]any)
	if !ok {
		return entry, fmt.Errorf("invalid json")
	}

	entry.ReviewCount = int(getNthElementAndCast[float64](darray, 4, 8))

	if onlyReviewCount {
		return entry, nil
	}

	entry.Link = getNthElementAndCast[string](darray, 27)
	entry.Title = getNthElementAndCast[string](darray, 11)

	categoriesI := getNthElementAndCast[[]any](darray, 13)

	entry.Categories = make([]string, len(categoriesI))
	for i := range categoriesI {
		entry.Categories[i], _ = categoriesI[i].(string)
	}

	if len(entry.Categories) > 0 {
		entry.Category = entry.Categories[0]
	}

	entry.Address = strings.TrimSpace(
		strings.TrimPrefix(getNthElementAndCast[string](darray, 18), entry.Title+","),
	)
	entry.OpenHours = getHours(darray)
	entry.PopularTimes = getPopularTimes(darray)
	entry.WebSite = getNthElementAndCast[string](darray, 7, 0)

	// If website is a social URL, populate legacy single fields used in CSV and arrays for parity
	if s := strings.ToLower(strings.TrimSpace(entry.WebSite)); s != "" {
		if strings.Contains(s, "facebook.com") {
			if entry.Facebook == "" {
				entry.Facebook = entry.WebSite
			}
			if entry.FacebookLinks == nil {
				entry.FacebookLinks = make([]string, 0, 1)
			}
			exists := false
			for _, v := range entry.FacebookLinks {
				if v == entry.WebSite {
					exists = true
					break
				}
			}
			if !exists {
				entry.FacebookLinks = append(entry.FacebookLinks, entry.WebSite)
			}
		} else if strings.Contains(s, "instagram.com") {
			if entry.Instagram == "" {
				entry.Instagram = entry.WebSite
			}
			if entry.InstagramLinks == nil {
				entry.InstagramLinks = make([]string, 0, 1)
			}
			exists := false
			for _, v := range entry.InstagramLinks {
				if v == entry.WebSite {
					exists = true
					break
				}
			}
			if !exists {
				entry.InstagramLinks = append(entry.InstagramLinks, entry.WebSite)
			}
		} else if strings.Contains(s, "linkedin.com") {
			if entry.LinkedIn == "" {
				entry.LinkedIn = entry.WebSite
			}
			if entry.LinkedInLinks == nil {
				entry.LinkedInLinks = make([]string, 0, 1)
			}
			exists := false
			for _, v := range entry.LinkedInLinks {
				if v == entry.WebSite {
					exists = true
					break
				}
			}
			if !exists {
				entry.LinkedInLinks = append(entry.LinkedInLinks, entry.WebSite)
			}
		} else if strings.Contains(s, "wa.me") || strings.Contains(s, "whatsapp.com") {
			if entry.WhatsApp == "" {
				entry.WhatsApp = entry.WebSite
			}
		}
	}

	entry.Phone = getNthElementAndCast[string](darray, 178, 0, 0)
	entry.PlusCode = getNthElementAndCast[string](darray, 183, 2, 2, 0)
	entry.ReviewRating = getNthElementAndCast[float64](darray, 4, 7)
	entry.Latitude = getNthElementAndCast[float64](darray, 9, 2)
	entry.Longtitude = getNthElementAndCast[float64](darray, 9, 3)
	entry.Cid = getNthElementAndCast[string](jd, 25, 3, 0, 13, 0, 0, 1)
	entry.Status = getNthElementAndCast[string](darray, 34, 4, 4)
	entry.Description = getNthElementAndCast[string](darray, 32, 1, 1)
	entry.ReviewsLink = getNthElementAndCast[string](darray, 4, 3, 0)
	entry.Thumbnail = getNthElementAndCast[string](darray, 72, 0, 1, 6, 0)
	entry.Timezone = getNthElementAndCast[string](darray, 30)
	entry.PriceRange = getNthElementAndCast[string](darray, 4, 2)
	entry.DataID = getNthElementAndCast[string](darray, 10)

	items := getLinkSource(getLinkSourceParams{
		arr:    getNthElementAndCast[[]any](darray, 171, 0),
		link:   []int{3, 0, 6, 0},
		source: []int{2},
	})

	entry.Images = make([]Image, len(items))

	for i := range items {
		entry.Images[i] = Image{
			Title: items[i].Source,
			Image: items[i].Link,
		}
	}

	entry.Reservations = getLinkSource(getLinkSourceParams{
		arr:    getNthElementAndCast[[]any](darray, 46),
		link:   []int{0},
		source: []int{1},
	})

	orderOnlineI := getNthElementAndCast[[]any](darray, 75, 0, 1, 2)

	if len(orderOnlineI) == 0 {
		orderOnlineI = getNthElementAndCast[[]any](darray, 75, 0, 0, 2)
	}

	entry.OrderOnline = getLinkSource(getLinkSourceParams{
		arr:    orderOnlineI,
		link:   []int{1, 2, 0},
		source: []int{0, 0},
	})

	entry.Menu = LinkSource{
		Link:   getNthElementAndCast[string](darray, 38, 0),
		Source: getNthElementAndCast[string](darray, 38, 1),
	}

	entry.Owner = Owner{
		ID:   getNthElementAndCast[string](darray, 57, 2),
		Name: getNthElementAndCast[string](darray, 57, 1),
	}

	if entry.Owner.ID != "" {
		entry.Owner.Link = fmt.Sprintf("https://www.google.com/maps/contrib/%s", entry.Owner.ID)
		// Heuristic: if there is an Owner ID, consider the business as claimed
		if entry.Claimed == "" {
			entry.Claimed = "YES"
		}
	}

	entry.CompleteAddress = Address{
		Borough:    getNthElementAndCast[string](darray, 183, 1, 0),
		Street:     getNthElementAndCast[string](darray, 183, 1, 1),
		City:       getNthElementAndCast[string](darray, 183, 1, 3),
		PostalCode: getNthElementAndCast[string](darray, 183, 1, 4),
		State:      getNthElementAndCast[string](darray, 183, 1, 5),
		Country:    getNthElementAndCast[string](darray, 183, 1, 6),
	}

	aboutI := getNthElementAndCast[[]any](darray, 100, 1)

	for i := range aboutI {
		el := getNthElementAndCast[[]any](aboutI, i)
		about := About{
			ID:   getNthElementAndCast[string](el, 0),
			Name: getNthElementAndCast[string](el, 1),
		}

		optsI := getNthElementAndCast[[]any](el, 2)

		for j := range optsI {
			opt := Option{
				Enabled: (getNthElementAndCast[float64](optsI, j, 2, 1, 0, 0)) == 1,
				Name:    getNthElementAndCast[string](optsI, j, 1),
			}

			if opt.Name != "" {
				about.Options = append(about.Options, opt)
			}
		}

		entry.About = append(entry.About, about)
	}

	entry.ReviewsPerRating = map[int]int{
		1: int(getNthElementAndCast[float64](darray, 175, 3, 0)),
		2: int(getNthElementAndCast[float64](darray, 175, 3, 1)),
		3: int(getNthElementAndCast[float64](darray, 175, 3, 2)),
		4: int(getNthElementAndCast[float64](darray, 175, 3, 3)),
		5: int(getNthElementAndCast[float64](darray, 175, 3, 4)),
	}

	reviewsI := getNthElementAndCast[[]any](darray, 175, 9, 0, 0)
	entry.UserReviews = make([]Review, 0, len(reviewsI))

	// Derived fields for richer dataset
	entry.GoogleMapsURL = entry.Link
	entry.FeaturedImage = selectFeaturedImage(&entry)

	// place_id from reviews link
	entry.PlaceID = extractPlaceIDFromReviewsLink(entry.ReviewsLink)
	if entry.PlaceID != "" {
		entry.ReviewURL = buildReviewURL(entry.PlaceID, "", "")
	}

	// kgmid from raw JSON graph scan
	if kg := scanKgmidFromJSONArr(jd); kg != "" {
		entry.Kgmid = kg
		entry.GoogleKnowledgeURL = fmt.Sprintf("https://www.google.com/search?kgmid=%s&kponly", kg)
	}

	// opening hours formatted string
	entry.OpeningHoursFormatted = formatOpeningHoursString(entry.OpenHours)

	// domain canonicalization
	entry.Domain = canonicalDomain(entry.WebSite)

	// phones normalization (best-effort; requires region hint)
	entry.Phones = normalizePhones(entry.Phone, entry.CompleteAddress.Country)

	// default claimed to NO unless enrichment sets YES later
	if entry.Claimed == "" {
		entry.Claimed = "NO"
	}

	return entry, nil
}

func parseReviews(reviewsI []any) []Review {
	ans := make([]Review, 0, len(reviewsI))

	for i := range reviewsI {
		el := getNthElementAndCast[[]any](reviewsI, i, 0)

		time := getNthElementAndCast[[]any](el, 2, 2, 0, 1, 21, 6, 8)

		profilePic, err := decodeURL(getNthElementAndCast[string](el, 1, 4, 5, 1))
		if err != nil {
			profilePic = ""
		}

		review := Review{
			Name:           getNthElementAndCast[string](el, 1, 4, 5, 0),
			ProfilePicture: profilePic,
			When: func() string {
				if len(time) < 3 {
					return ""
				}

				return fmt.Sprintf("%v-%v-%v", time[0], time[1], time[2])
			}(),
			Rating:      int(getNthElementAndCast[float64](el, 2, 0, 0)),
			Description: getNthElementAndCast[string](el, 2, 15, 0, 0),
		}

		if review.Name == "" {
			continue
		}

		optsI := getNthElementAndCast[[]any](el, 2, 2, 0, 1, 21, 7)

		for j := range optsI {
			val := getNthElementAndCast[string](optsI, j)
			if val != "" {
				review.Images = append(review.Images, val[2:])
			}
		}

		ans = append(ans, review)
	}

	return ans
}

type getLinkSourceParams struct {
	arr    []any
	source []int
	link   []int
}

func getLinkSource(params getLinkSourceParams) []LinkSource {
	var result []LinkSource

	for i := range params.arr {
		item := getNthElementAndCast[[]any](params.arr, i)

		el := LinkSource{
			Source: getNthElementAndCast[string](item, params.source...),
			Link:   getNthElementAndCast[string](item, params.link...),
		}
		if el.Link != "" && el.Source != "" {
			result = append(result, el)
		}
	}

	return result
}

//nolint:gomnd // it's ok, I need the indexes
func getHours(darray []any) map[string][]string {
	items := getNthElementAndCast[[]any](darray, 34, 1)
	hours := make(map[string][]string, len(items))

	for _, item := range items {
		//nolint:errcheck // it's ok, I'm "sure" the indexes are correct
		day := getNthElementAndCast[string](item.([]any), 0)
		//nolint:errcheck // it's ok, I'm "sure" the indexes are correct
		timesI := getNthElementAndCast[[]any](item.([]any), 1)
		times := make([]string, len(timesI))

		for i := range timesI {
			times[i], _ = timesI[i].(string)
		}

		hours[day] = times
	}

	return hours
}

func getPopularTimes(darray []any) map[string]map[int]int {
	items := getNthElementAndCast[[]any](darray, 84, 0) //nolint:gomnd // it's ok, I need the indexes
	popularTimes := make(map[string]map[int]int, len(items))

	dayOfWeek := map[int]string{
		1: "Monday",
		2: "Tuesday",
		3: "Wednesday",
		4: "Thursday",
		5: "Friday",
		6: "Saturday",
		7: "Sunday",
	}

	for ii := range items {
		item, ok := items[ii].([]any)
		if !ok {
			return nil
		}

		day := int(getNthElementAndCast[float64](item, 0))

		timesI := getNthElementAndCast[[]any](item, 1)

		times := make(map[int]int, len(timesI))

		for i := range timesI {
			t, ok := timesI[i].([]any)
			if !ok {
				return nil
			}

			v, ok := t[1].(float64)
			if !ok {
				return nil
			}

			h, ok := t[0].(float64)
			if !ok {
				return nil
			}

			times[int(h)] = int(v)
		}

		popularTimes[dayOfWeek[day]] = times
	}

	return popularTimes
}

func getNthElementAndCast[T any](arr []any, indexes ...int) T {
	var (
		defaultVal T
		idx        int
	)

	if len(indexes) == 0 {
		return defaultVal
	}

	for len(indexes) > 1 {
		idx, indexes = indexes[0], indexes[1:]

		if idx >= len(arr) {
			return defaultVal
		}

		next := arr[idx]

		if next == nil {
			return defaultVal
		}

		var ok bool

		arr, ok = next.([]any)
		if !ok {
			return defaultVal
		}
	}

	if len(indexes) == 0 || len(arr) == 0 {
		return defaultVal
	}

	ans, ok := arr[indexes[0]].(T)
	if !ok {
		return defaultVal
	}

	return ans
}

func stringSliceToString(s []string) string {
	return strings.Join(s, ", ")
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%f", val)
	case nil:
		return ""
	default:
		d, _ := json.Marshal(v)
		return string(d)
	}
}

func decodeURL(url string) (string, error) {
	quoted := `"` + strings.ReplaceAll(url, `"`, `\"`) + `"`

	unquoted, err := strconv.Unquote(quoted)
	if err != nil {
		return "", fmt.Errorf("failed to decode URL: %v", err)
	}

	return unquoted, nil
}

type EntryWithDistance struct {
	Entry    *Entry
	Distance float64
}

func filterAndSortEntriesWithinRadius(entries []*Entry, lat, lon, radius float64) []*Entry {
	withinRadiusIterator := func(yield func(EntryWithDistance) bool) {
		for _, entry := range entries {
			distance := entry.haversineDistance(lat, lon)
			if distance <= radius {
				if !yield(EntryWithDistance{Entry: entry, Distance: distance}) {
					return
				}
			}
		}
	}

	entriesWithDistance := slices.Collect(iter.Seq[EntryWithDistance](withinRadiusIterator))

	slices.SortFunc(entriesWithDistance, func(a, b EntryWithDistance) int {
		switch {
		case a.Distance < b.Distance:
			return -1
		case a.Distance > b.Distance:
			return 1
		default:
			return 0
		}
	})

	resultIterator := func(yield func(*Entry) bool) {
		for _, e := range entriesWithDistance {
			if !yield(e.Entry) {
				return
			}
		}
	}

	return slices.Collect(iter.Seq[*Entry](resultIterator))
}

// -------- Derived helpers for normalization and enrichment --------

func formatOpeningHoursString(hours map[string][]string) string {
	if len(hours) == 0 {
		return ""
	}
	order := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	var parts []string
	for _, day := range order {
		if times, ok := hours[day]; ok {
			// Normalize "Open 24 hours" into bracketed format
			for i := range times {
				times[i] = strings.TrimSpace(times[i])
			}
			item := fmt.Sprintf("%s: [%s]", day, strings.Join(times, ", "))
			parts = append(parts, item)
		}
	}
	return strings.Join(parts, ", ")
}

func canonicalDomain(site string) string {
	site = strings.TrimSpace(site)
	if site == "" {
		return ""
	}
	s := site
	// Ensure parsable URL
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		host := strings.TrimSpace(site)
		host = strings.ToLower(host)
		host = strings.TrimPrefix(host, "www.")
		return host
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	return host
}

func normalizePhones(phone, country string) []string {
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, e := range out {
			if e == v {
				return
			}
		}
		out = append(out, v)
	}

	s := strings.TrimSpace(phone)
	if s == "" {
		return out
	}

	// Best-effort digit extraction (keep '+' if present)
	digits := func(in string) string {
		var b strings.Builder
		for _, r := range in {
			if (r >= '0' && r <= '9') || r == '+' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}(s)

	// Country calling code map
	countryCallingCode := func(region string) string {
		switch strings.ToUpper(region) {
		case "US", "CA":
			return "+1"
		case "ID":
			return "+62"
		case "SG":
			return "+65"
		case "MY":
			return "+60"
		case "PH":
			return "+63"
		case "TH":
			return "+66"
		case "VN":
			return "+84"
		case "IN":
			return "+91"
		case "GB":
			return "+44"
		case "AU":
			return "+61"
		default:
			return ""
		}
	}

	region := countryToRegion(strings.TrimSpace(country))
	cc := countryCallingCode(region)

	// National (preserve input formatting)
	add(s)

	// International representation
	if strings.HasPrefix(digits, "+") {
		add(digits)
	} else if cc != "" {
		add(cc + " " + digits)
		add(strings.ReplaceAll(cc+digits, " ", ""))
	} else {
		add(digits)
	}

	// E.164 (best effort)
	if strings.HasPrefix(digits, "+") {
		add(digits)
	} else if cc != "" {
		add(strings.ReplaceAll(cc+digits, " ", ""))
	}

	return out
}

func countryToRegion(country string) string {
	if country == "" {
		return "US"
	}
	// If already ISO-2 code
	if len(country) == 2 {
		return strings.ToUpper(country)
	}
	switch strings.ToLower(strings.TrimSpace(country)) {
	case "united states", "usa", "u.s.a.":
		return "US"
	case "indonesia":
		return "ID"
	case "singapore":
		return "SG"
	case "malaysia":
		return "MY"
	case "philippines":
		return "PH"
	case "thailand":
		return "TH"
	case "vietnam":
		return "VN"
	case "india":
		return "IN"
	case "united kingdom", "uk", "great britain":
		return "GB"
	case "canada":
		return "CA"
	case "australia":
		return "AU"
	default:
		return "US"
	}
}

func selectFeaturedImage(e *Entry) string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Thumbnail) != "" {
		return e.Thumbnail
	}
	if len(e.Images) > 0 {
		if strings.TrimSpace(e.Images[0].Image) != "" {
			return e.Images[0].Image
		}
	}
	return ""
}

func extractPlaceIDFromReviewsLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return u.Query().Get("placeid")
}

func scanKgmidFromJSONArr(arr []any) string {
	if len(arr) == 0 {
		return ""
	}
	re := regexp.MustCompile(`kgmid=\/g\/([A-Za-z0-9]+)`)
	var ans string
	var walk func([]any)
	walk = func(a []any) {
		for _, v := range a {
			if ans != "" {
				return
			}
			switch t := v.(type) {
			case string:
				m := re.FindStringSubmatch(t)
				if len(m) == 2 {
					ans = "/g/" + m[1]
					return
				}
			case []any:
				walk(t)
			default:
				// ignore
			}
		}
	}
	walk(arr)
	return ans
}

func buildReviewURL(placeID, lang, country string) string {
	if strings.TrimSpace(placeID) == "" {
		return ""
	}
	base := "https://search.google.com/local/reviews?placeid=" + placeID + "&authuser=0"
	if strings.TrimSpace(lang) != "" {
		base += "&hl=" + strings.TrimSpace(lang)
	}
	if strings.TrimSpace(country) != "" {
		base += "&gl=" + strings.TrimSpace(country)
	}
	return base
}
