package api

import (
	"database/sql"
	"net/http"
	"strings"

	"kiro-proxy/store"
)

func (s *Server) handleModelGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		groups, err := s.db.GetModelGroups()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, groups)

	case http.MethodPost:
		var group store.ModelGroup
		if err := readJSON(r, &group); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		group.Name = strings.TrimSpace(group.Name)
		if err := s.validateModelGroupName(group.Name, ""); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := s.db.CreateModelGroup(&group); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.router.Reload()
		writeJSON(w, http.StatusCreated, group)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModelGroupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.healthProvider == nil {
		writeOK(w, []store.ModelHealthStatus{})
		return
	}
	writeOK(w, s.healthProvider.ModelHealthStatuses())
}

func (s *Server) handleModelGroup(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/model-groups/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing model group id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		group, err := s.db.GetModelGroup(id)
		if err != nil {
			if err == sql.ErrNoRows {
				writeError(w, http.StatusNotFound, "model group not found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, group)

	case http.MethodPut:
		var group store.ModelGroup
		if err := readJSON(r, &group); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		group.ID = id
		group.Name = strings.TrimSpace(group.Name)
		if err := s.validateModelGroupName(group.Name, id); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if err := s.db.UpdateModelGroup(&group); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.router.Reload()
		writeOK(w, group)

	case http.MethodDelete:
		if err := s.db.DeleteModelGroup(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.router.Reload()
		writeOK(w, map[string]bool{"ok": true})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) validateModelGroupName(name, excludeID string) error {
	if strings.TrimSpace(name) == "" {
		return &modelGroupValidationError{message: "分组名称不能为空"}
	}
	if existing, _ := s.db.FindModelGroupByName(name); existing != nil && existing.ID != excludeID {
		return &modelGroupValidationError{message: "分组名称已存在"}
	}
	models, err := s.db.GetModels()
	if err != nil {
		return err
	}
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Name), name) ||
			strings.EqualFold(strings.TrimSpace(model.ModelID), name) {
			return &modelGroupValidationError{message: "分组名称不能与模型名称重复"}
		}
	}
	return nil
}

func (s *Server) validateModelGroupConflict(values ...string) error {
	groups, err := s.db.GetModelGroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" && strings.EqualFold(group.Name, value) {
				return &modelGroupValidationError{message: "模型名称或 model_id 不能与分组名称重复"}
			}
		}
	}
	return nil
}

type modelGroupValidationError struct {
	message string
}

func (e *modelGroupValidationError) Error() string {
	return e.message
}
