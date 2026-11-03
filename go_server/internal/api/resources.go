package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"traccar-server_go/go_server/internal/auth"
	"traccar-server_go/go_server/internal/geofence"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/permissions"
)

// maxResourceBody caps a configuration body. It is larger than the device
// limit because a calendar carries a whole iCalendar document.
const maxResourceBody = 1 << 20

// ResourceStore is the storage contract of the configuration routes: the CRUD
// of every resource the Java server exposes through SimpleObjectResource and
// ExtendedObjectResource, plus the permission pairs that decide who reaches
// them. *storage.Store satisfies it.
type ResourceStore interface {
	permissions.Store

	Groups(ctx context.Context, filter model.ResourceFilter) ([]*model.Group, error)
	CreateGroup(ctx context.Context, group *model.Group) error
	UpdateGroup(ctx context.Context, group *model.Group) error
	DeleteGroup(ctx context.Context, groupID int64) error

	QueryGeofences(ctx context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error)
	CreateGeofence(ctx context.Context, fence *geofence.Geofence) error
	UpdateGeofence(ctx context.Context, fence *geofence.Geofence) error
	DeleteGeofence(ctx context.Context, geofenceID int64) error

	Calendars(ctx context.Context, filter model.ResourceFilter) ([]*model.Calendar, error)
	CreateCalendar(ctx context.Context, calendar *model.Calendar) error
	UpdateCalendar(ctx context.Context, calendar *model.Calendar) error
	DeleteCalendar(ctx context.Context, calendarID int64) error

	Drivers(ctx context.Context, filter model.ResourceFilter) ([]*model.Driver, error)
	CreateDriver(ctx context.Context, driver *model.Driver) error
	UpdateDriver(ctx context.Context, driver *model.Driver) error
	DeleteDriver(ctx context.Context, driverID int64) error

	Maintenances(ctx context.Context, filter model.ResourceFilter) ([]*model.Maintenance, error)
	CreateMaintenance(ctx context.Context, maintenance *model.Maintenance) error
	UpdateMaintenance(ctx context.Context, maintenance *model.Maintenance) error
	DeleteMaintenance(ctx context.Context, maintenanceID int64) error

	Orders(ctx context.Context, filter model.ResourceFilter) ([]*model.Order, error)
	CreateOrder(ctx context.Context, order *model.Order) error
	UpdateOrder(ctx context.Context, order *model.Order) error
	DeleteOrder(ctx context.Context, orderID int64) error

	Reports(ctx context.Context, filter model.ResourceFilter) ([]*model.Report, error)
	CreateReport(ctx context.Context, report *model.Report) error
	UpdateReport(ctx context.Context, report *model.Report) error
	DeleteReport(ctx context.Context, reportID int64) error

	Attributes(ctx context.Context, filter model.ResourceFilter) ([]*model.Attribute, error)
	CreateAttribute(ctx context.Context, attribute *model.Attribute) error
	UpdateAttribute(ctx context.Context, attribute *model.Attribute) error
	DeleteAttribute(ctx context.Context, attributeID int64) error
}

// WithResources enables the configuration routes: /api/groups, /api/geofences,
// /api/calendars, /api/drivers, /api/maintenance(s), /api/orders, /api/reports
// and /api/attributes/computed. Without it every one of them reports 501.
func WithResources(store ResourceStore) Option {
	return func(s *Server) {
		if store == nil {
			return
		}
		s.resources = store
		// Every route has to answer "may this caller see object N", so a
		// deployment that enables the resources gets the permission service
		// built from the same store.
		if s.permissions == nil {
			s.permissions = permissions.NewService(store)
		}
	}
}

// resourceQuery is the subset of the SimpleObjectResource and
// ExtendedObjectResource parameters that applies to every resource.
type resourceQuery struct {
	All               bool
	UserID            int64
	GroupID           int64
	DeviceID          int64
	ExcludeAttributes bool
	Limit             int
	Offset            int
	Keyword           string
}

func parseResourceQuery(r *http.Request) (resourceQuery, error) {
	values := r.URL.Query()

	all, err := boolQuery(values, "all")
	if err != nil {
		return resourceQuery{}, err
	}
	excludeAttributes, err := boolQuery(values, "excludeAttributes")
	if err != nil {
		return resourceQuery{}, err
	}
	userID, err := idQuery(values, "userId")
	if err != nil {
		return resourceQuery{}, err
	}
	groupID, err := idQuery(values, "groupId")
	if err != nil {
		return resourceQuery{}, err
	}
	deviceID, err := idQuery(values, "deviceId")
	if err != nil {
		return resourceQuery{}, err
	}
	limit, err := intQuery(values, "limit")
	if err != nil {
		return resourceQuery{}, err
	}
	offset, err := intQuery(values, "offset")
	if err != nil {
		return resourceQuery{}, err
	}
	return resourceQuery{
		All:               all,
		UserID:            userID,
		GroupID:           groupID,
		DeviceID:          deviceID,
		ExcludeAttributes: excludeAttributes,
		Limit:             limit,
		Offset:            offset,
		Keyword:           values.Get("keyword"),
	}, nil
}

func boolQuery(values url.Values, name string) (bool, error) {
	raw := values.Get(name)
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

func idQuery(values url.Values, name string) (int64, error) {
	raw := values.Get(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

func intQuery(values url.Values, name string) (int, error) {
	raw := values.Get(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

// idFilter narrows a list query to a set of ids. restricted distinguishes "no
// condition" from "matches nothing", which the storage layer also separates
// through a nil versus an empty slice.
type idFilter struct {
	restricted bool
	ids        []int64
}

func (f idFilter) intersect(ids []int64) idFilter {
	if !f.restricted {
		return idFilter{restricted: true, ids: append([]int64(nil), ids...)}
	}
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	kept := make([]int64, 0, len(f.ids))
	for _, id := range f.ids {
		if _, ok := wanted[id]; ok {
			kept = append(kept, id)
		}
	}
	return idFilter{restricted: true, ids: kept}
}

func (f idFilter) slice() []int64 {
	if !f.restricted {
		return nil
	}
	if f.ids == nil {
		return []int64{}
	}
	return f.ids
}

// resourceScope resolves the ids a list call may return, applying the owner,
// group and device conditions of the Java implementation.
func (s *Server) resourceScope(ctx context.Context, r *http.Request, query resourceQuery, object permissions.Object) (idFilter, error) {
	filter, err := s.ownedResourceIDs(ctx, r, query, object)
	if err != nil {
		return idFilter{}, err
	}

	if query.GroupID > 0 {
		if err := s.checkObjectAccess(ctx, r, permissions.ObjectGroup, query.GroupID); err != nil {
			return idFilter{}, err
		}
		ids, err := s.permissionPropertyIDs(ctx, permissions.ObjectGroup, query.GroupID, object)
		if err != nil {
			return idFilter{}, err
		}
		filter = filter.intersect(ids)
	}
	if query.DeviceID > 0 {
		if err := s.checkObjectAccess(ctx, r, permissions.ObjectDevice, query.DeviceID); err != nil {
			return idFilter{}, err
		}
		ids, err := s.permissionPropertyIDs(ctx, permissions.ObjectDevice, query.DeviceID, object)
		if err != nil {
			return idFilter{}, err
		}
		filter = filter.intersect(ids)
	}
	return filter, nil
}

// ownedResourceIDs is the userId condition: without all a caller sees its own
// objects, with all an admin sees every row. An admin may also name another
// account with userId, which is what PermissionsService.checkUser allows.
func (s *Server) ownedResourceIDs(ctx context.Context, r *http.Request, query resourceQuery, object permissions.Object) (idFilter, error) {
	if !s.authEnabled() {
		return idFilter{}, nil
	}
	principal := auth.FromContext(r.Context())
	if principal == nil {
		return idFilter{}, nil
	}
	if principal.Admin && principal.UserID == 0 {
		// The shared API key holds no account: it owns nothing, so the "own
		// objects" default cannot apply to it.
		return idFilter{}, nil
	}
	if query.All && principal.Admin {
		return idFilter{}, nil
	}

	userID := principal.UserID
	// With all the Java server ignores userId; without it the parameter names
	// the account whose grants are listed, which only an admin may point
	// elsewhere.
	if !query.All && query.UserID > 0 {
		if query.UserID != userID && !principal.Admin {
			return idFilter{}, fmt.Errorf("%w: user %d", permissions.ErrAccessDenied, query.UserID)
		}
		userID = query.UserID
	}
	if s.permissions == nil {
		return idFilter{}, errors.New("permissions are not configured")
	}
	ids, err := s.permissions.ObjectIDs(ctx, userID, object)
	if err != nil {
		return idFilter{}, err
	}
	return idFilter{restricted: true, ids: ids}, nil
}

// checkObjectAccess reports whether the caller may read or change one object.
func (s *Server) checkObjectAccess(ctx context.Context, r *http.Request, object permissions.Object, objectID int64) error {
	if !s.authEnabled() {
		return nil
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.Admin {
		return nil
	}
	if s.permissions == nil {
		return errors.New("permissions are not configured")
	}
	allowed, err := s.permissions.CanAccess(ctx, principal.UserID, false, object, objectID)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("%w: %s %d", permissions.ErrAccessDenied, object, objectID)
	}
	return nil
}

// permissionPropertyIDs lists the objects of one type granted directly to an
// owner. It is the groupId and deviceId condition: the Java server asks for the
// pairs of that group or device without following the group tree.
func (s *Server) permissionPropertyIDs(ctx context.Context, owner permissions.Object, ownerID int64, property permissions.Object) ([]int64, error) {
	if s.permissions == nil {
		return nil, errors.New("permissions are not configured")
	}
	pairs, err := s.permissions.Permissions(ctx, owner, ownerID, property, 0)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(pairs))
	for _, pair := range pairs {
		ids = append(ids, pair.PropertyID)
	}
	return ids, nil
}

// grantCreated writes the owner grant the Java server adds right after an
// insert, so the creator reaches the row without a separate share. The legacy
// shared key is not an account and grants nothing.
func (s *Server) grantCreated(ctx context.Context, r *http.Request, object permissions.Object, objectID int64) error {
	if !s.authEnabled() {
		return nil
	}
	principal := auth.FromContext(r.Context())
	if principal == nil || principal.UserID <= 0 {
		return nil
	}
	if s.permissions == nil {
		return errors.New("permissions are not configured")
	}
	pair, err := permissions.New(permissions.ObjectUser, principal.UserID, object, objectID)
	if err != nil {
		return err
	}
	return s.permissions.Grant(ctx, pair)
}

// resource binds one model type to its storage calls, the permission object its
// grants live under and its JSON translation. The five handlers below are
// written once against it.
type resource[T any] struct {
	object     permissions.Object
	readScope  auth.Scope
	writeScope auth.Scope
	// adminWrite marks the resources the Java server only lets an admin change.
	adminWrite bool

	list   func(context.Context, model.ResourceFilter) ([]*T, error)
	create func(context.Context, *T) error
	update func(context.Context, *T) error
	remove func(context.Context, int64) error

	decode func(http.ResponseWriter, *http.Request) (*T, error)
	setID  func(*T, int64)
	id     func(*T) int64
	render func(*T, bool) any
	// validate checks the references a write introduces, such as the parent of
	// a group or the calendar of a geofence.
	validate func(context.Context, *Server, *http.Request, *T) error
}

// resourceRoutes is one mounted resource: the path and the five handlers.
type resourceRoutes struct {
	path   string
	list   http.HandlerFunc
	get    http.HandlerFunc
	create http.HandlerFunc
	update http.HandlerFunc
	remove http.HandlerFunc
}

// registerResourceRoutes mounts every configuration resource. Maintenance is
// reachable in both spellings: the Java server calls the route
// /api/maintenance and the milestone notes call it /api/maintenances.
func (s *Server) registerResourceRoutes(mux *http.ServeMux) {
	routes := []resourceRoutes{
		resourceRoutesFor(s, "/api/groups", s.groupResource()),
		resourceRoutesFor(s, "/api/geofences", s.geofenceResource()),
		resourceRoutesFor(s, "/api/calendars", s.calendarResource()),
		resourceRoutesFor(s, "/api/drivers", s.driverResource()),
		resourceRoutesFor(s, "/api/maintenance", s.maintenanceResource()),
		resourceRoutesFor(s, "/api/maintenances", s.maintenanceResource()),
		resourceRoutesFor(s, "/api/orders", s.orderResource()),
		resourceRoutesFor(s, "/api/reports", s.reportResource()),
		resourceRoutesFor(s, "/api/attributes/computed", s.attributeResource()),
	}
	for _, route := range routes {
		mux.HandleFunc("GET "+route.path, route.list)
		mux.HandleFunc("GET "+route.path+"/{id}", route.get)
		mux.HandleFunc("POST "+route.path, route.create)
		mux.HandleFunc("PUT "+route.path+"/{id}", route.update)
		mux.HandleFunc("DELETE "+route.path+"/{id}", route.remove)
	}
}

func resourceRoutesFor[T any](s *Server, path string, res *resource[T]) resourceRoutes {
	return resourceRoutes{
		path:   path,
		list:   listResource(s, res),
		get:    getResource(s, res),
		create: createResource(s, res),
		update: updateResource(s, res),
		remove: removeResource(s, res),
	}
}

func listResource[T any](s *Server, res *resource[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.resourcesConfigured(w) {
			return
		}
		if !s.authorize(w, r, res.readScope) {
			return
		}
		query, err := parseResourceQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()

		scope, err := s.resourceScope(ctx, r, query, res.object)
		if err != nil {
			s.writeResourceError(w, err)
			return
		}

		values, err := res.list(ctx, model.ResourceFilter{
			IDs:     scope.slice(),
			Keyword: query.Keyword,
			Limit:   query.Limit,
			Offset:  query.Offset,
		})
		if err != nil {
			s.logf("api: list %s: %v", res.object, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}

		includeAttributes := !query.ExcludeAttributes
		response := make([]any, 0, len(values))
		for _, value := range values {
			response = append(response, res.render(value, includeAttributes))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func getResource[T any](s *Server, res *resource[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.resourcesConfigured(w) {
			return
		}
		if !s.authorize(w, r, res.readScope) {
			return
		}
		id, err := positiveID(r, "id")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()

		if err := s.checkObjectAccess(ctx, r, res.object, id); err != nil {
			s.writeResourceError(w, err)
			return
		}
		values, err := res.list(ctx, model.ResourceFilter{IDs: []int64{id}, Limit: 1})
		if err != nil {
			s.logf("api: get %s %d: %v", res.object, id, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		if len(values) == 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown %s %d", res.object, id))
			return
		}
		writeJSON(w, http.StatusOK, res.render(values[0], true))
	}
}

func createResource[T any](s *Server, res *resource[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.resourcesConfigured(w) {
			return
		}
		if !s.authorize(w, r, res.writeScope) {
			return
		}
		if res.adminWrite && !s.isAdmin(r) {
			writeError(w, http.StatusForbidden,
				fmt.Errorf("%w: only an admin may change %s", permissions.ErrAccessDenied, res.object))
			return
		}
		value, err := res.decode(w, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()

		if res.validate != nil {
			if err := res.validate(ctx, s, r, value); err != nil {
				s.writeResourceError(w, err)
				return
			}
		}
		if err := res.create(ctx, value); err != nil {
			s.logf("api: create %s: %v", res.object, err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}

		if err := s.grantCreated(ctx, r, res.object, res.id(value)); err != nil {
			s.logf("api: grant %s %d: %v", res.object, res.id(value), err)
			// Without the grant the creator could not see the row it just
			// wrote, so undo the insert instead of leaving it orphaned.
			if cleanupErr := res.remove(ctx, res.id(value)); cleanupErr != nil {
				s.logf("api: rollback %s %d: %v", res.object, res.id(value), cleanupErr)
			}
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		s.audit(r, model.ActionCreate, string(res.object), res.id(value))
		writeJSON(w, http.StatusCreated, res.render(value, true))
	}
}

func updateResource[T any](s *Server, res *resource[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.resourcesConfigured(w) {
			return
		}
		if !s.authorize(w, r, res.writeScope) {
			return
		}
		if res.adminWrite && !s.isAdmin(r) {
			writeError(w, http.StatusForbidden,
				fmt.Errorf("%w: only an admin may change %s", permissions.ErrAccessDenied, res.object))
			return
		}
		id, err := positiveID(r, "id")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()

		if err := s.checkObjectAccess(ctx, r, res.object, id); err != nil {
			s.writeResourceError(w, err)
			return
		}
		value, err := res.decode(w, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		res.setID(value, id)

		if res.validate != nil {
			if err := res.validate(ctx, s, r, value); err != nil {
				s.writeResourceError(w, err)
				return
			}
		}
		if err := res.update(ctx, value); err != nil {
			s.writeResourceError(w, err)
			return
		}
		s.audit(r, model.ActionEdit, string(res.object), res.id(value))
		writeJSON(w, http.StatusOK, res.render(value, true))
	}
}

func removeResource[T any](s *Server, res *resource[T]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.resourcesConfigured(w) {
			return
		}
		if !s.authorize(w, r, res.writeScope) {
			return
		}
		if res.adminWrite && !s.isAdmin(r) {
			writeError(w, http.StatusForbidden,
				fmt.Errorf("%w: only an admin may change %s", permissions.ErrAccessDenied, res.object))
			return
		}
		id, err := positiveID(r, "id")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
		defer cancel()

		if err := s.checkObjectAccess(ctx, r, res.object, id); err != nil {
			s.writeResourceError(w, err)
			return
		}
		if err := res.remove(ctx, id); err != nil {
			s.writeResourceError(w, err)
			return
		}
		s.audit(r, model.ActionRemove, string(res.object), id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// resourcesConfigured reports whether the configuration routes are enabled.
func (s *Server) resourcesConfigured(w http.ResponseWriter) bool {
	if s.resources == nil {
		writeError(w, http.StatusNotImplemented, errors.New("resources are not configured"))
		return false
	}
	return true
}

// writeResourceError maps the errors the resource handlers produce onto status
// codes. An inaccessible object is always 403, never 404, so a caller cannot
// probe which ids exist.
func (s *Server) writeResourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, permissions.ErrAccessDenied):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, permissions.ErrGroupDepth),
		errors.Is(err, permissions.ErrUnknownGroup),
		errors.Is(err, permissions.ErrInvalidPermission):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, model.ErrResourceNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		s.logf("api: resource: %v", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

func decodeResourceBody[T any](w http.ResponseWriter, r *http.Request) (*T, error) {
	var body T
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResourceBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("invalid body: %w", err)
	}
	return &body, nil
}

// optionalAttributes keeps the field out of the answer when the caller asked
// for excludeAttributes, and renders an empty object otherwise so clients can
// read it without a nil check.
func optionalAttributes(attributes model.Attributes, include bool) *model.Attributes {
	if !include {
		return nil
	}
	if attributes == nil {
		attributes = model.Attributes{}
	}
	return &attributes
}

// validateGroupWrite checks the parent a group write introduces: the caller has
// to reach it and the tree has to stay inside permissions.MaxGroupDepth.
func validateGroupWrite(ctx context.Context, s *Server, r *http.Request, group *model.Group) error {
	if group.GroupID <= 0 {
		return nil
	}
	if err := s.checkObjectAccess(ctx, r, permissions.ObjectGroup, group.GroupID); err != nil {
		return err
	}
	if s.resources == nil {
		return nil
	}
	parents, err := s.resources.GroupParents(ctx)
	if err != nil {
		return err
	}
	return permissions.CheckHierarchy(parents, group.ID, group.GroupID)
}

// validateCalendarWrite checks that the caller may use the calendar a geofence
// or report is scheduled on.
func validateCalendarWrite(ctx context.Context, s *Server, r *http.Request, calendarID int64) error {
	if calendarID <= 0 {
		return nil
	}
	return s.checkObjectAccess(ctx, r, permissions.ObjectCalendar, calendarID)
}

type groupBody struct {
	Name       string           `json:"name"`
	GroupID    int64            `json:"groupId"`
	Attributes model.Attributes `json:"attributes"`
}

type groupValue struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	GroupID    int64             `json:"groupId"`
	Attributes *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) groupResource() *resource[model.Group] {
	return &resource[model.Group]{
		object:     permissions.ObjectGroup,
		readScope:  auth.ScopeReadGroups,
		writeScope: auth.ScopeWriteGroups,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Group, error) {
			return s.resources.Groups(ctx, filter)
		},
		create: func(ctx context.Context, group *model.Group) error {
			return s.resources.CreateGroup(ctx, group)
		},
		update: func(ctx context.Context, group *model.Group) error {
			return s.resources.UpdateGroup(ctx, group)
		},
		remove: func(ctx context.Context, groupID int64) error {
			return s.resources.DeleteGroup(ctx, groupID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Group, error) {
			body, err := decodeResourceBody[groupBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Group{Name: body.Name, GroupID: body.GroupID, Attributes: body.Attributes}, nil
		},
		setID:    func(group *model.Group, id int64) { group.ID = id },
		id:       func(group *model.Group) int64 { return group.ID },
		validate: validateGroupWrite,
		render: func(group *model.Group, includeAttributes bool) any {
			return groupValue{
				ID:         group.ID,
				Name:       group.Name,
				GroupID:    group.GroupID,
				Attributes: optionalAttributes(group.Attributes, includeAttributes),
			}
		},
	}
}

type geofenceBody struct {
	CalendarID  int64            `json:"calendarId"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Area        string           `json:"area"`
	Attributes  model.Attributes `json:"attributes"`
}

type geofenceValue struct {
	ID          int64             `json:"id"`
	CalendarID  int64             `json:"calendarId"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Area        string            `json:"area"`
	Attributes  *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) geofenceResource() *resource[geofence.Geofence] {
	return &resource[geofence.Geofence]{
		object:     permissions.ObjectGeofence,
		readScope:  auth.ScopeReadGeofences,
		writeScope: auth.ScopeWriteGeofences,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*geofence.Geofence, error) {
			return s.resources.QueryGeofences(ctx, filter)
		},
		create: func(ctx context.Context, fence *geofence.Geofence) error {
			return s.resources.CreateGeofence(ctx, fence)
		},
		update: func(ctx context.Context, fence *geofence.Geofence) error {
			return s.resources.UpdateGeofence(ctx, fence)
		},
		remove: func(ctx context.Context, geofenceID int64) error {
			return s.resources.DeleteGeofence(ctx, geofenceID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*geofence.Geofence, error) {
			body, err := decodeResourceBody[geofenceBody](w, r)
			if err != nil {
				return nil, err
			}
			area, err := geofence.Parse(body.Area)
			if err != nil {
				return nil, fmt.Errorf("invalid area: %w", err)
			}
			return &geofence.Geofence{
				CalendarID:  body.CalendarID,
				Name:        body.Name,
				Description: body.Description,
				Area:        area,
				Attributes:  body.Attributes,
			}, nil
		},
		setID: func(fence *geofence.Geofence, id int64) { fence.ID = id },
		id:    func(fence *geofence.Geofence) int64 { return fence.ID },
		validate: func(ctx context.Context, s *Server, r *http.Request, fence *geofence.Geofence) error {
			return validateCalendarWrite(ctx, s, r, fence.CalendarID)
		},
		render: func(fence *geofence.Geofence, includeAttributes bool) any {
			return geofenceValue{
				ID:          fence.ID,
				CalendarID:  fence.CalendarID,
				Name:        fence.Name,
				Description: fence.Description,
				Area:        fence.WKT(),
				Attributes:  optionalAttributes(fence.Attributes, includeAttributes),
			}
		},
	}
}

type calendarBody struct {
	Name       string           `json:"name"`
	Data       []byte           `json:"data"`
	Attributes model.Attributes `json:"attributes"`
}

type calendarValue struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Data       []byte            `json:"data"`
	Attributes *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) calendarResource() *resource[model.Calendar] {
	return &resource[model.Calendar]{
		object:     permissions.ObjectCalendar,
		readScope:  auth.ScopeReadCalendars,
		writeScope: auth.ScopeWriteCalendars,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Calendar, error) {
			return s.resources.Calendars(ctx, filter)
		},
		create: func(ctx context.Context, calendar *model.Calendar) error {
			return s.resources.CreateCalendar(ctx, calendar)
		},
		update: func(ctx context.Context, calendar *model.Calendar) error {
			return s.resources.UpdateCalendar(ctx, calendar)
		},
		remove: func(ctx context.Context, calendarID int64) error {
			return s.resources.DeleteCalendar(ctx, calendarID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Calendar, error) {
			body, err := decodeResourceBody[calendarBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Calendar{Name: body.Name, Data: body.Data, Attributes: body.Attributes}, nil
		},
		setID: func(calendar *model.Calendar, id int64) { calendar.ID = id },
		id:    func(calendar *model.Calendar) int64 { return calendar.ID },
		render: func(calendar *model.Calendar, includeAttributes bool) any {
			return calendarValue{
				ID:         calendar.ID,
				Name:       calendar.Name,
				Data:       calendar.Data,
				Attributes: optionalAttributes(calendar.Attributes, includeAttributes),
			}
		},
	}
}

type driverBody struct {
	Name       string           `json:"name"`
	UniqueID   string           `json:"uniqueId"`
	Attributes model.Attributes `json:"attributes"`
}

type driverValue struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	UniqueID   string            `json:"uniqueId"`
	Attributes *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) driverResource() *resource[model.Driver] {
	return &resource[model.Driver]{
		object:     permissions.ObjectDriver,
		readScope:  auth.ScopeReadDrivers,
		writeScope: auth.ScopeWriteDrivers,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Driver, error) {
			return s.resources.Drivers(ctx, filter)
		},
		create: func(ctx context.Context, driver *model.Driver) error {
			return s.resources.CreateDriver(ctx, driver)
		},
		update: func(ctx context.Context, driver *model.Driver) error {
			return s.resources.UpdateDriver(ctx, driver)
		},
		remove: func(ctx context.Context, driverID int64) error {
			return s.resources.DeleteDriver(ctx, driverID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Driver, error) {
			body, err := decodeResourceBody[driverBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Driver{Name: body.Name, UniqueID: body.UniqueID, Attributes: body.Attributes}, nil
		},
		setID: func(driver *model.Driver, id int64) { driver.ID = id },
		id:    func(driver *model.Driver) int64 { return driver.ID },
		render: func(driver *model.Driver, includeAttributes bool) any {
			return driverValue{
				ID:         driver.ID,
				Name:       driver.Name,
				UniqueID:   driver.UniqueID,
				Attributes: optionalAttributes(driver.Attributes, includeAttributes),
			}
		},
	}
}

type maintenanceBody struct {
	Name       string           `json:"name"`
	Type       string           `json:"type"`
	Start      float64          `json:"start"`
	Period     float64          `json:"period"`
	Attributes model.Attributes `json:"attributes"`
}

type maintenanceValue struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Start      float64           `json:"start"`
	Period     float64           `json:"period"`
	Attributes *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) maintenanceResource() *resource[model.Maintenance] {
	return &resource[model.Maintenance]{
		object:     permissions.ObjectMaintenance,
		readScope:  auth.ScopeReadMaintenances,
		writeScope: auth.ScopeWriteMaintenances,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Maintenance, error) {
			return s.resources.Maintenances(ctx, filter)
		},
		create: func(ctx context.Context, maintenance *model.Maintenance) error {
			return s.resources.CreateMaintenance(ctx, maintenance)
		},
		update: func(ctx context.Context, maintenance *model.Maintenance) error {
			return s.resources.UpdateMaintenance(ctx, maintenance)
		},
		remove: func(ctx context.Context, maintenanceID int64) error {
			return s.resources.DeleteMaintenance(ctx, maintenanceID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Maintenance, error) {
			body, err := decodeResourceBody[maintenanceBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Maintenance{
				Name:       body.Name,
				Type:       body.Type,
				Start:      body.Start,
				Period:     body.Period,
				Attributes: body.Attributes,
			}, nil
		},
		setID: func(maintenance *model.Maintenance, id int64) { maintenance.ID = id },
		id:    func(maintenance *model.Maintenance) int64 { return maintenance.ID },
		render: func(maintenance *model.Maintenance, includeAttributes bool) any {
			return maintenanceValue{
				ID:         maintenance.ID,
				Name:       maintenance.Name,
				Type:       maintenance.Type,
				Start:      maintenance.Start,
				Period:     maintenance.Period,
				Attributes: optionalAttributes(maintenance.Attributes, includeAttributes),
			}
		},
	}
}

type orderBody struct {
	UniqueID    string           `json:"uniqueId"`
	Description string           `json:"description"`
	FromAddress string           `json:"fromAddress"`
	ToAddress   string           `json:"toAddress"`
	Attributes  model.Attributes `json:"attributes"`
}

type orderValue struct {
	ID          int64             `json:"id"`
	UniqueID    string            `json:"uniqueId"`
	Description string            `json:"description"`
	FromAddress string            `json:"fromAddress"`
	ToAddress   string            `json:"toAddress"`
	Attributes  *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) orderResource() *resource[model.Order] {
	return &resource[model.Order]{
		object:     permissions.ObjectOrder,
		readScope:  auth.ScopeReadOrders,
		writeScope: auth.ScopeWriteOrders,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Order, error) {
			return s.resources.Orders(ctx, filter)
		},
		create: func(ctx context.Context, order *model.Order) error {
			return s.resources.CreateOrder(ctx, order)
		},
		update: func(ctx context.Context, order *model.Order) error {
			return s.resources.UpdateOrder(ctx, order)
		},
		remove: func(ctx context.Context, orderID int64) error {
			return s.resources.DeleteOrder(ctx, orderID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Order, error) {
			body, err := decodeResourceBody[orderBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Order{
				UniqueID:    body.UniqueID,
				Description: body.Description,
				FromAddress: body.FromAddress,
				ToAddress:   body.ToAddress,
				Attributes:  body.Attributes,
			}, nil
		},
		setID: func(order *model.Order, id int64) { order.ID = id },
		id:    func(order *model.Order) int64 { return order.ID },
		render: func(order *model.Order, includeAttributes bool) any {
			return orderValue{
				ID:          order.ID,
				UniqueID:    order.UniqueID,
				Description: order.Description,
				FromAddress: order.FromAddress,
				ToAddress:   order.ToAddress,
				Attributes:  optionalAttributes(order.Attributes, includeAttributes),
			}
		},
	}
}

type reportBody struct {
	CalendarID  int64            `json:"calendarId"`
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Attributes  model.Attributes `json:"attributes"`
}

type reportValue struct {
	ID          int64             `json:"id"`
	CalendarID  int64             `json:"calendarId"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Attributes  *model.Attributes `json:"attributes,omitempty"`
}

func (s *Server) reportResource() *resource[model.Report] {
	return &resource[model.Report]{
		object:     permissions.ObjectReport,
		readScope:  auth.ScopeReadReports,
		writeScope: auth.ScopeWriteReports,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Report, error) {
			return s.resources.Reports(ctx, filter)
		},
		create: func(ctx context.Context, report *model.Report) error {
			return s.resources.CreateReport(ctx, report)
		},
		update: func(ctx context.Context, report *model.Report) error {
			return s.resources.UpdateReport(ctx, report)
		},
		remove: func(ctx context.Context, reportID int64) error {
			return s.resources.DeleteReport(ctx, reportID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Report, error) {
			body, err := decodeResourceBody[reportBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Report{
				CalendarID:  body.CalendarID,
				Type:        body.Type,
				Description: body.Description,
				Attributes:  body.Attributes,
			}, nil
		},
		setID: func(report *model.Report, id int64) { report.ID = id },
		id:    func(report *model.Report) int64 { return report.ID },
		validate: func(ctx context.Context, s *Server, r *http.Request, report *model.Report) error {
			return validateCalendarWrite(ctx, s, r, report.CalendarID)
		},
		render: func(report *model.Report, includeAttributes bool) any {
			return reportValue{
				ID:          report.ID,
				CalendarID:  report.CalendarID,
				Type:        report.Type,
				Description: report.Description,
				Attributes:  optionalAttributes(report.Attributes, includeAttributes),
			}
		},
	}
}

type attributeBody struct {
	Description string `json:"description"`
	Attribute   string `json:"attribute"`
	Expression  string `json:"expression"`
	Type        string `json:"type"`
	Priority    int    `json:"priority"`
}

type attributeValue struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
	Attribute   string `json:"attribute"`
	Expression  string `json:"expression"`
	Type        string `json:"type"`
	Priority    int    `json:"priority"`
}

func (s *Server) attributeResource() *resource[model.Attribute] {
	return &resource[model.Attribute]{
		object:     permissions.ObjectAttribute,
		readScope:  auth.ScopeReadAttributes,
		writeScope: auth.ScopeWriteAttributes,
		// A computed attribute runs against every position, so the Java server
		// only lets an admin define one.
		adminWrite: true,
		list: func(ctx context.Context, filter model.ResourceFilter) ([]*model.Attribute, error) {
			return s.resources.Attributes(ctx, filter)
		},
		create: func(ctx context.Context, attribute *model.Attribute) error {
			return s.resources.CreateAttribute(ctx, attribute)
		},
		update: func(ctx context.Context, attribute *model.Attribute) error {
			return s.resources.UpdateAttribute(ctx, attribute)
		},
		remove: func(ctx context.Context, attributeID int64) error {
			return s.resources.DeleteAttribute(ctx, attributeID)
		},
		decode: func(w http.ResponseWriter, r *http.Request) (*model.Attribute, error) {
			body, err := decodeResourceBody[attributeBody](w, r)
			if err != nil {
				return nil, err
			}
			return &model.Attribute{
				Description: body.Description,
				Attribute:   body.Attribute,
				Expression:  body.Expression,
				Type:        body.Type,
				Priority:    body.Priority,
			}, nil
		},
		setID: func(attribute *model.Attribute, id int64) { attribute.ID = id },
		id:    func(attribute *model.Attribute) int64 { return attribute.ID },
		render: func(attribute *model.Attribute, _ bool) any {
			return attributeValue{
				ID:          attribute.ID,
				Description: attribute.Description,
				Attribute:   attribute.Attribute,
				Expression:  attribute.Expression,
				Type:        attribute.Type,
				Priority:    attribute.Priority,
			}
		},
	}
}
