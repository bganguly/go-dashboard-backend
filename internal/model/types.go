package model

type RegionDTO struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type CustomerSummaryDTO struct {
	ID        int    `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

type CustomerDTO struct {
	ID        int       `json:"id"`
	Email     string    `json:"email"`
	FirstName string    `json:"firstName"`
	LastName  string    `json:"lastName"`
	Phone     *string   `json:"phone"`
	Region    RegionDTO `json:"region"`
	CreatedAt *string   `json:"createdAt"`
}

type CustomerListResult struct {
	Data       []CustomerDTO `json:"data"`
	NextCursor *int          `json:"nextCursor"`
	HasMore    bool          `json:"hasMore"`
}

type ProductSummaryDTO struct {
	ID   int    `json:"id"`
	SKU  string `json:"sku"`
	Name string `json:"name"`
}

type OrderItemDTO struct {
	ID        int               `json:"id"`
	ProductID int               `json:"productId"`
	Quantity  int               `json:"quantity"`
	UnitPrice float64           `json:"unitPrice"`
	Discount  float64           `json:"discount"`
	Product   ProductSummaryDTO `json:"product"`
}

type OrderDTO struct {
	ID        int                `json:"id"`
	Status    string             `json:"status"`
	Total     float64            `json:"total"`
	Currency  string             `json:"currency"`
	Notes     *string            `json:"notes"`
	PlacedAt  string             `json:"placedAt"`
	Customer  CustomerSummaryDTO `json:"customer"`
	Region    RegionDTO          `json:"region"`
	Items     []OrderItemDTO     `json:"items"`
}

type OrderListResult struct {
	Data        []OrderDTO `json:"data"`
	Page        int        `json:"page"`
	PageSize    int        `json:"pageSize"`
	Total       int64      `json:"total"`
	TotalPages  int        `json:"totalPages"`
	Approximate bool       `json:"approximate"`
}

type CategoryAggregateDTO struct {
	TotalOrders   int64   `json:"totalOrders"`
	TotalRevenue  float64 `json:"totalRevenue"`
	TotalItems    int64   `json:"totalItems"`
	AvgOrderValue float64 `json:"avgOrderValue"`
}

type TotalsDTO struct {
	TotalOrders  int64   `json:"totalOrders"`
	TotalRevenue float64 `json:"totalRevenue"`
	TotalItems   int64   `json:"totalItems"`
}

type DailyAggregateDTO struct {
	Date       string                          `json:"date"`
	Categories map[string]CategoryAggregateDTO `json:"categories"`
	Totals     TotalsDTO                       `json:"totals"`
}

type CreateOrderItem struct {
	ProductID int     `json:"productId" binding:"required"`
	Quantity  int     `json:"quantity"  binding:"required"`
	UnitPrice float64 `json:"unitPrice" binding:"required"`
	Discount  float64 `json:"discount"`
}

type CreateOrderRequest struct {
	CustomerID int               `json:"customerId" binding:"required"`
	RegionID   int               `json:"regionId"   binding:"required"`
	Currency   string            `json:"currency"`
	Notes      string            `json:"notes"`
	Items      []CreateOrderItem `json:"items"      binding:"required,min=1"`
}
