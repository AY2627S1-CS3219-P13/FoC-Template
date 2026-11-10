package order

import (
	"context"
	"math"
)

// Coordinate is a campus location's WGS84 position and display name, owned by
// Supplier Service.
type Coordinate struct {
	Latitude, Longitude float64
	Name                string
}

// Coordinates maps every campus location ID to its position. A nil value means
// the location exists but Supplier has no coordinates for it.
func (c *CatalogueClient) Coordinates(ctx context.Context, session string) (map[string]*Coordinate, error) {
	body, err := c.locations(ctx, session)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Coordinate, len(body.Locations))
	for _, l := range body.Locations {
		result[l.ID] = nil
		if l.Latitude != nil {
			result[l.ID] = &Coordinate{Latitude: *l.Latitude, Longitude: *l.Longitude, Name: l.Name}
		}
	}
	return result, nil
}

// earthRadiusMeters is the mean Earth radius; web/core.mjs uses the same value.
const earthRadiusMeters = 6371000

// distanceMeters is the straight-line (Haversine) distance rounded to a metre.
// It ignores paths and building entrances, so it is an estimate for ranking.
func distanceMeters(a, b Coordinate) int64 {
	radians := func(degrees float64) float64 { return degrees * math.Pi / 180 }
	dLat := radians(b.Latitude - a.Latitude)
	dLng := radians(b.Longitude - a.Longitude)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(radians(a.Latitude))*math.Cos(radians(b.Latitude))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return int64(math.Round(2 * earthRadiusMeters * math.Asin(math.Min(1, math.Sqrt(h)))))
}

// pickupLocationID is the campus location of a pickup: the place itself, or the
// supplier's registered location from the order's latest snapshot.
func pickupLocationID(o Order) string {
	if o.Pickup.Type == "location" {
		return o.Pickup.ID
	}
	return o.Pickup.LocationID
}

// openOrder adds the courier-relative distance to an open-list entry.
type openOrder struct {
	Order
	PickupDistanceMeters *int64 `json:"pickupDistanceMeters,omitempty"`
}
