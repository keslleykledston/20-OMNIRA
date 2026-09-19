# OMNIRA Pagination & Filtering

**Release:** R0.2  
**Ticket:** T23  
**Version:** 0.1.0

## Overview

OMNIRA implements cursor-based pagination with:
- **Cursor-based navigation**: stateless, efficient pagination using encoded ID+timestamp
- **Query filtering**: advanced filtering with multiple operators
- **Sorting**: configurable sort direction (asc/desc)
- **HTTP headers**: pagination metadata in response headers
- **Safe defaults**: limit clamped to 100 max, 20 per default

---

## Cursor-Based Pagination

### Why Cursor-Based?

Traditional offset-based pagination has issues:
- **Inefficient**: requires scanning all rows up to offset
- **Inconsistent**: new items inserted during pagination
- **Non-scalable**: O(n) query cost as offset grows

Cursor-based pagination:
- **Stateless**: cursor encodes position (ID + timestamp)
- **Consistent**: unaffected by concurrent inserts
- **Efficient**: uses indexed scans

### Cursor Format

Cursors are opaque, base64-encoded strings:

```
eyJ1c2VyLTEyMyI6MTY5NTMxMjM0NX0=
```

When decoded:
```
user-123:1695312345
```

Format: `{ID}:{Unix Timestamp}`

### Using Pagination

**Request:**
```
GET /api/v1/tenants/memberships?limit=20&cursor=abc123&sort=created_at:desc
```

**Query Parameters:**
- `limit` (int): items per page (default 20, max 100)
- `cursor` (string): cursor for next page (empty for first page)
- `sort` (string): sort field and direction (format: `field:asc|desc`)

**Response:**
```json
{
  "items": [
    { "id": "user-1", "name": "Alice", "created_at": "2024-09-18T12:00:00Z" },
    { "id": "user-2", "name": "Bob", "created_at": "2024-09-18T11:59:00Z" }
  ],
  "next_cursor": "dXNlci0yOjE2OTUzMDk5NDA=",
  "has_more": true,
  "count": 2,
  "limit": 20
}
```

**Response Headers:**
```
X-Pagination-Count: 2
X-Pagination-Limit: 20
X-Pagination-HasMore: true
X-Pagination-NextCursor: dXNlci0yOjE2OTUzMDk5NDA=
```

---

## Filtering

### Filter Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `eq` | Equals | `status:eq:active` |
| `ne` | Not equals | `status:ne:inactive` |
| `gt` | Greater than | `created_at:gt:2024-01-01` |
| `gte` | Greater or equal | `score:gte:100` |
| `lt` | Less than | `created_at:lt:2024-12-31` |
| `lte` | Less or equal | `score:lte:1000` |
| `in` | In list | `status:in:active,pending` |
| `contains` | Contains substring | `name:contains:alice` |

### Query Parameters

Filters are passed as query parameters:

```
GET /api/v1/tenants/memberships?status:eq=active&role:in=admin,editor&limit=50
```

---

## Sorting

### Sort Format

Sort specification: `{field}:{direction}`

- `field`: database column name
- `direction`: `asc` or `desc`

Examples:
```
sort=created_at:desc          # newest first
sort=name:asc                 # alphabetical
sort=updated_at:desc&limit=50 # combine with pagination
```

### Default Sorting

If no sort specified, results sorted by creation time (descending):
```
created_at:desc
```

---

## API Integration

### In HTTP Handlers

```go
import "github.com/omnira/omnira/internal/platform/pagination"

func ListMemberships(w http.ResponseWriter, r *http.Request) {
	// Parse pagination options
	opts := pagination.ParsePageOptionsFromQuery(r)
	
	// opts.Limit, opts.Cursor, opts.Sort
	
	// Fetch data from repository with pagination
	items, nextCursor, hasMore, err := repo.FindWithPagination(
		ctx,
		tenantID,
		opts.Limit+1, // fetch limit+1 to detect has_more
		opts.Cursor,
		opts.Sort,
	)
	
	// Build result
	result := pagination.NewPageResult(
		toResponses(items),
		opts.Limit,
		nextCursor,
		hasMore,
	)
	
	// Write response
	w.Header().Set("Content-Type", "application/json")
	pagination.WritePaginationHeaders(w, result)
	json.NewEncoder(w).Encode(result)
}
```

### In Repositories

```go
// FindWithPagination returns items + next cursor
func (r *Repository) FindWithPagination(
	ctx context.Context,
	tenantID uuid.UUID,
	limit int,
	cursor string,
	sort string,
) (items []*Item, nextCursor string, hasMore bool, err error) {
	
	// Decode cursor
	decodedCursor, _ := pagination.DecodeCursor(cursor)
	
	// Parse sort
	sortSpec, _ := pagination.ParseSort(sort)
	
	// Build query
	query := "SELECT id, name, created_at FROM items WHERE tenant_id = $1"
	args := []interface{}{tenantID}
	
	// Apply cursor filter (where created_at < cursor_time OR (created_at = cursor_time AND id > cursor_id))
	if decodedCursor != nil {
		query += " AND (created_at, id) > ($2, $3)"
		args = append(args, decodedCursor.Timestamp, decodedCursor.ID)
	}
	
	// Apply sort
	if sortSpec != nil {
		query += fmt.Sprintf(" ORDER BY %s %s", sortSpec.Field, sortSpec.Direction)
	} else {
		query += " ORDER BY created_at DESC"
	}
	
	// Fetch limit+1 to detect has_more
	query += fmt.Sprintf(" LIMIT %d", limit+1)
	
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	
	// Scan items
	for rows.Next() && len(items) < limit {
		var item Item
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt); err != nil {
			return nil, "", false, err
		}
		items = append(items, &item)
	}
	
	// Detect has_more
	hasMore = len(items) > limit
	if hasMore {
		items = items[:limit] // remove extra item
	}
	
	// Encode next cursor
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = (&pagination.Cursor{
			ID:        last.ID.String(),
			Timestamp: last.CreatedAt,
		}).Encode()
	}
	
	return items, nextCursor, hasMore, nil
}
```

---

## Performance

### Memory Usage
- Cursor: ~50 bytes (base64 encoded)
- PageResult: ~1 KB per page
- No session state required

### Query Efficiency
- Indexed cursor queries: O(log n) lookup + O(k) scan (k = page size)
- Efficient for large datasets
- No performance cliff as data grows

### Recommended Indexes
```sql
-- For cursor-based pagination
CREATE INDEX idx_items_tenant_created ON items(tenant_id, created_at DESC, id);
CREATE INDEX idx_items_tenant_updated ON items(tenant_id, updated_at DESC, id);
```

---

## Testing

### Unit Tests (12 passing)

✓ Cursor encoding/decoding  
✓ Cursor nil handling  
✓ Invalid cursor format  
✓ Sort parsing (asc/desc)  
✓ Page result construction  
✓ Query parameter parsing  
✓ Limit clamping (max 100)  
✓ Invalid limit fallback  
✓ Pagination headers  

### Integration Patterns

```go
// Test cursor progression
cursor1 := ""
for i := 0; i < 10; i++ {
	result, _ := handler.FetchPage(ctx, cursor1, 20)
	if !result.HasMore {
		break
	}
	cursor1 = result.NextCursor
}

// Test filtering + sorting
result, _ := handler.FetchPage(
	ctx,
	"",
	20,
	filter="status:eq:active",
	sort="created_at:desc",
)
```

---

## Future Enhancements (R0.3+)

1. **Advanced Filtering**: AND/OR operators, nested filters
2. **Full-Text Search**: indexed LIKE searches
3. **Aggregations**: COUNT, SUM, GROUP BY support
4. **Export**: CSV/JSON export with full filters
5. **GraphQL**: GraphQL cursor pagination compatibility
6. **Caching**: cursor position caching for repeated queries

---

## Related Documentation

- **Rate Limiting:** [`docs/RATE-LIMITING.md`](./RATE-LIMITING.md)
- **API Endpoints:** See OpenAPI spec for pagination query params
- **Architecture:** [`docs/architecture/API-GOVERNANCE.md`](./architecture/API-GOVERNANCE.md)
