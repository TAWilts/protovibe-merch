package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tawilts/protovibe-merch/backend/internal/audit"
	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/catalogue"
)

func (s *Server) registerCatalogueRoutes(g *gin.RouterGroup) {
	// Sellers need to read the assortment; only managers may change it.
	read := g.Group("/articles", requireAuth(), requireBandRole(models.RoleSeller))
	read.GET("", s.listArticles)
	read.GET("/:id", s.getArticle)

	write := g.Group("/articles", requireAuth(), requireBandRole(models.RoleManager))
	write.POST("", s.createArticle)
	write.PUT("/:id", s.saveArticle)

	g.GET("/assortment", requireAuth(), requireBandRole(models.RoleSeller), s.getAssortment)
}

// variantPayload is one variant as the API returns it, with its derived stock
// already resolved so the client never has to compute it.
type variantPayload struct {
	ID                        int64   `json:"id"`
	OptionValueIDs            []int64 `json:"option_value_ids"`
	CombinationKey            string  `json:"combination_key"`
	SalePriceCents            int64   `json:"sale_price_cents"`
	DefaultPurchasePriceCents int64   `json:"default_purchase_price_cents"`
	MinimumStock              *int    `json:"minimum_stock"`
	IsOffered                 bool    `json:"is_offered"`
	NoReorder                 bool    `json:"no_reorder"`
	IsActive                  bool    `json:"is_active"`

	Purchased    int64 `json:"purchased"`
	Sold         int64 `json:"sold"`
	OnHand       int64 `json:"on_hand"`
	BelowMinimum bool  `json:"below_minimum"`

	// PhotoIDs are the variant's pictures in display order. Only the ids
	// travel; the files are fetched separately so the point-of-sale payload
	// stays small on a weak connection.
	PhotoIDs []int64 `json:"photo_ids"`
}

type optionValuePayload struct {
	ID       int64  `json:"id"`
	Value    string `json:"value"`
	Position int    `json:"position"`
	IsActive bool   `json:"is_active"`
}

type optionGroupPayload struct {
	ID       int64                `json:"id"`
	Name     string               `json:"name"`
	Position int                  `json:"position"`
	IsActive bool                 `json:"is_active"`
	Values   []optionValuePayload `json:"values"`
}

type articlePayload struct {
	ID                        int64  `json:"id"`
	Name                      string `json:"name"`
	DefaultSalePriceCents     int64  `json:"default_sale_price_cents"`
	DefaultPurchasePriceCents int64  `json:"default_purchase_price_cents"`
	IsOffered                 bool   `json:"is_offered"`
	IsActive                  bool   `json:"is_active"`
	// ConfigurationComplete is false while an option group still has no
	// values; such an article cannot be sold yet.
	ConfigurationComplete bool                 `json:"configuration_complete"`
	TotalStock            int64                `json:"total_stock"`
	OptionGroups          []optionGroupPayload `json:"option_groups"`
	Variants              []variantPayload     `json:"variants"`
}

// listArticles returns the full catalogue including inactive variants, which
// the management page needs to show what was retired.
func (s *Server) listArticles(c *gin.Context) {
	payload, err := s.buildArticles(c, 0)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"articles": payload})
}

func (s *Server) getArticle(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	payload, err := s.buildArticles(c, id)
	if err != nil {
		serverError(c, err)
		return
	}
	if len(payload) == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such article")
		return
	}
	c.JSON(http.StatusOK, payload[0])
}

// getAssortment returns only what can actually be sold right now, which keeps
// the point-of-sale payload small on a phone with a weak connection.
func (s *Server) getAssortment(c *gin.Context) {
	all, err := s.buildArticles(c, 0)
	if err != nil {
		serverError(c, err)
		return
	}

	offered := make([]articlePayload, 0, len(all))
	for _, article := range all {
		if !article.IsOffered || !article.IsActive || !article.ConfigurationComplete {
			continue
		}
		variants := make([]variantPayload, 0, len(article.Variants))
		for _, variant := range article.Variants {
			if variant.IsActive && variant.IsOffered {
				variants = append(variants, variant)
			}
		}
		if len(variants) == 0 {
			continue
		}
		article.Variants = variants
		offered = append(offered, article)
	}

	c.JSON(http.StatusOK, gin.H{
		"articles":        offered,
		"payment_methods": models.PaymentMethods,
	})
}

// buildArticles assembles the payload for one article or, with id 0, all of
// them. It resolves stock once for the whole band rather than per variant.
func (s *Server) buildArticles(c *gin.Context, id int64) ([]articlePayload, error) {
	ctx := c.Request.Context()

	articleQuery := s.db.WithContext(ctx).Model(&models.Article{}).Where("is_active = ?", true)
	if id != 0 {
		articleQuery = articleQuery.Where("id = ?", id)
	}
	var articles []models.Article
	if err := articleQuery.Order("name").Find(&articles).Error; err != nil {
		return nil, err
	}
	if len(articles) == 0 {
		return []articlePayload{}, nil
	}

	articleIDs := make([]int64, len(articles))
	for i, article := range articles {
		articleIDs[i] = article.ID
	}

	var groups []models.OptionGroup
	if err := s.db.WithContext(ctx).Where("article_id IN ?", articleIDs).
		Order("position, id").Find(&groups).Error; err != nil {
		return nil, err
	}
	groupIDs := make([]int64, len(groups))
	for i, group := range groups {
		groupIDs[i] = group.ID
	}

	var values []models.OptionValue
	if len(groupIDs) > 0 {
		if err := s.db.WithContext(ctx).Where("option_group_id IN ?", groupIDs).
			Order("position, id").Find(&values).Error; err != nil {
			return nil, err
		}
	}

	var variants []models.Variant
	if err := s.db.WithContext(ctx).Where("article_id IN ?", articleIDs).
		Order("combination_key").Find(&variants).Error; err != nil {
		return nil, err
	}

	stock, err := s.catalogue.StockMap(ctx)
	if err != nil {
		return nil, err
	}

	var photos []models.VariantPhoto
	if err := s.db.WithContext(ctx).
		Select("id", "variant_id").
		Where("variant_id IN (?)",
			s.db.WithContext(ctx).Model(&models.Variant{}).
				Select("id").Where("article_id IN ?", articleIDs)).
		Order("position, id").Find(&photos).Error; err != nil {
		return nil, err
	}
	photosByVariant := map[int64][]int64{}
	for _, photo := range photos {
		photosByVariant[photo.VariantID] = append(photosByVariant[photo.VariantID], photo.ID)
	}
	// A variant without pictures must still send an empty list rather than
	// null, so the client never has to guard the field.
	emptyPhotos := func(id int64) []int64 {
		if ids := photosByVariant[id]; ids != nil {
			return ids
		}
		return []int64{}
	}

	valuesByGroup := map[int64][]optionValuePayload{}
	for _, value := range values {
		valuesByGroup[value.OptionGroupID] = append(valuesByGroup[value.OptionGroupID], optionValuePayload{
			ID: value.ID, Value: value.Value, Position: value.Position, IsActive: value.IsActive,
		})
	}
	groupsByArticle := map[int64][]optionGroupPayload{}
	for _, group := range groups {
		groupsByArticle[group.ArticleID] = append(groupsByArticle[group.ArticleID], optionGroupPayload{
			ID: group.ID, Name: group.Name, Position: group.Position, IsActive: group.IsActive,
			Values: valuesByGroup[group.ID],
		})
	}

	variantsByArticle := map[int64][]variantPayload{}
	totalStock := map[int64]int64{}
	for _, variant := range variants {
		position := stock[variant.ID]
		payload := variantPayload{
			ID:                        variant.ID,
			OptionValueIDs:            variant.OptionValueIDs,
			CombinationKey:            variant.CombinationKey,
			SalePriceCents:            variant.SalePriceCents,
			DefaultPurchasePriceCents: variant.DefaultPurchasePriceCents,
			MinimumStock:              variant.MinimumStock,
			PhotoIDs:                  emptyPhotos(variant.ID),
			IsOffered:                 variant.IsOffered,
			NoReorder:                 variant.NoReorder,
			IsActive:                  variant.IsActive,
			Purchased:                 position.Purchased,
			Sold:                      position.Sold,
			OnHand:                    position.OnHand,
			BelowMinimum:              catalogue.IsAtOrBelowMinimum(position.OnHand, variant.MinimumStock),
		}
		if payload.OptionValueIDs == nil {
			payload.OptionValueIDs = []int64{}
		}
		variantsByArticle[variant.ArticleID] = append(variantsByArticle[variant.ArticleID], payload)
		if variant.IsActive {
			totalStock[variant.ArticleID] += position.OnHand
		}
	}

	out := make([]articlePayload, 0, len(articles))
	for _, article := range articles {
		articleGroups := groupsByArticle[article.ID]
		complete := true
		for _, group := range articleGroups {
			if !group.IsActive {
				continue
			}
			active := 0
			for _, value := range group.Values {
				if value.IsActive {
					active++
				}
			}
			if active == 0 {
				complete = false
			}
		}
		if articleGroups == nil {
			articleGroups = []optionGroupPayload{}
		}
		articleVariants := variantsByArticle[article.ID]
		if articleVariants == nil {
			articleVariants = []variantPayload{}
		}

		out = append(out, articlePayload{
			ID:                        article.ID,
			Name:                      article.Name,
			DefaultSalePriceCents:     article.DefaultSalePriceCents,
			DefaultPurchasePriceCents: article.DefaultPurchasePriceCents,
			IsOffered:                 article.IsOffered,
			IsActive:                  article.IsActive,
			ConfigurationComplete:     complete,
			TotalStock:                totalStock[article.ID],
			OptionGroups:              articleGroups,
			Variants:                  articleVariants,
		})
	}
	return out, nil
}

type createArticleRequest struct {
	Name                      string `json:"name" binding:"required"`
	DefaultSalePriceCents     int64  `json:"default_sale_price_cents"`
	DefaultPurchasePriceCents int64  `json:"default_purchase_price_cents"`
}

func (s *Server) createArticle(c *gin.Context) {
	var req createArticleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	article, err := s.catalogue.CreateArticle(ctx, req.Name, req.DefaultSalePriceCents, req.DefaultPurchasePriceCents)
	if err != nil {
		s.reportCatalogueError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionArticleCreated, EntityType: "article", EntityID: &article.ID,
		Details: map[string]any{"name": article.Name},
	})

	payload, err := s.buildArticles(c, article.ID)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(http.StatusCreated, payload[0])
}

func (s *Server) saveArticle(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}

	var cfg catalogue.ArticleConfiguration
	if err := c.ShouldBindJSON(&cfg); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	ctx := c.Request.Context()
	if err := s.catalogue.ApplyConfiguration(ctx, id, cfg); err != nil {
		s.reportCatalogueError(c, err)
		return
	}

	s.audit.Log(ctx, actorFrom(c), audit.Entry{
		Action: audit.ActionArticleUpdated, EntityType: "article", EntityID: &id,
	})

	payload, err := s.buildArticles(c, id)
	if err != nil {
		serverError(c, err)
		return
	}
	if len(payload) == 0 {
		fail(c, http.StatusNotFound, "not_found", "no such article")
		return
	}
	c.JSON(http.StatusOK, payload[0])
}

// reportCatalogueError maps the service errors onto stable API codes.
func (s *Server) reportCatalogueError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, catalogue.ErrArticleNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		fail(c, http.StatusNotFound, "not_found", "no such article")
	case errors.Is(err, catalogue.ErrInvalidName):
		fail(c, http.StatusBadRequest, "invalid_name", err.Error())
	case errors.Is(err, catalogue.ErrNegativePrice):
		fail(c, http.StatusBadRequest, "invalid_price", err.Error())
	case errors.Is(err, catalogue.ErrUnknownEntity):
		fail(c, http.StatusBadRequest, "unknown_entity", err.Error())
	default:
		serverError(c, err)
	}
}

// pathID reads a numeric :id path parameter.
func pathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid_id", "invalid identifier")
		return 0, false
	}
	return id, true
}
