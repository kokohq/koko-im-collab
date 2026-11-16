package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/graphql-go/graphql"
)

// kokoGraphQLSchema is built once at startup and reused for all requests.
var kokoGraphQLSchema graphql.Schema

// initGraphQLSchema constructs the GraphQL schema with SummaryType and ActPromptType.
// Must be called after the store is available (i.e., in NewWebhookHandler or main).
func initGraphQLSchema(h *WebhookHandler) {
	summaryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Summary",
		Fields: graphql.Fields{
			"id":             &graphql.Field{Type: graphql.Int},
			"account_id":     &graphql.Field{Type: graphql.String},
			"messenger_type": &graphql.Field{Type: graphql.String},
			"chat_id":        &graphql.Field{Type: graphql.String},
			"domain":         &graphql.Field{Type: graphql.String},
			"window_start": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if s, ok := p.Source.(MessengerSummary); ok {
					return s.WindowStart.UTC().Format(time.RFC3339), nil
				}
				return nil, nil
			}},
			"window_end": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if s, ok := p.Source.(MessengerSummary); ok {
					return s.WindowEnd.UTC().Format(time.RFC3339), nil
				}
				return nil, nil
			}},
			"source_message_count": &graphql.Field{Type: graphql.Int},
			"summary_text":         &graphql.Field{Type: graphql.String},
			"summarized_at": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if s, ok := p.Source.(MessengerSummary); ok {
					return s.SummarizedAt.UTC().Format(time.RFC3339), nil
				}
				return nil, nil
			}},
		},
	})

	actPromptType := graphql.NewObject(graphql.ObjectConfig{
		Name: "ActPrompt",
		Fields: graphql.Fields{
			"id":          &graphql.Field{Type: graphql.Int},
			"account_id":  &graphql.Field{Type: graphql.String},
			"user_name":   &graphql.Field{Type: graphql.String},
			"group_id":    &graphql.Field{Type: graphql.String},
			"group_name":  &graphql.Field{Type: graphql.String},
			"domain":      &graphql.Field{Type: graphql.String},
			"prompt_text": &graphql.Field{Type: graphql.String},
			"created_at": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				if a, ok := p.Source.(KokoActPrompt); ok {
					return a.CreatedAt.UTC().Format(time.RFC3339), nil
				}
				return nil, nil
			}},
		},
	})

	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{

			// summaries(accountId, groupName, domain, from, to, page, pageSize)
			"summaries": &graphql.Field{
				Type: graphql.NewList(summaryType),
				Args: graphql.FieldConfigArgument{
					"accountId": &graphql.ArgumentConfig{Type: graphql.String},
					"groupName": &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"domain":    &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"from":      &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"to":        &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"page":      &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 1},
					"pageSize":  &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 5},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					ctx, ok := p.Context.Value(ctxGraphQLRequest).(*graphQLRequestCtx)
					if !ok || ctx == nil {
						return nil, errGraphQLUnauthed
					}
					accountID := stringArg(p.Args, "accountId", ctx.Key.AccountID)
					if accountID != ctx.Key.AccountID {
						return nil, errGraphQLForbidden
					}
					groupName := stringArg(p.Args, "groupName", "")
					domain := apiNormDomain(stringArg(p.Args, "domain", ""))
					from, to, err := parseAPIDateRange(stringArg(p.Args, "from", ""), stringArg(p.Args, "to", ""))
					if err != nil {
						return nil, err
					}
					page := intArgGQL(p.Args, "page", 1)
					pageSize := intArgGQL(p.Args, "pageSize", apiMaxPageSize)
					if pageSize > apiMaxPageSize {
						pageSize = apiMaxPageSize
					}
					results, _, err := h.store.ListSummaries(context.Background(), accountID, groupName, domain, from, to, page, pageSize)
					if err != nil {
						log.Printf("GQL_LIST_SUMMARIES_FAILED: account=%s error=%v", accountID, err)
						return nil, err
					}
					// Convert to interface{} slice for graphql-go
					out := make([]interface{}, len(results))
					for i, r := range results {
						out[i] = r
					}
					return out, nil
				},
			},

			// actPrompts(accountId, groupName, domain, from, to, page, pageSize)
			"actPrompts": &graphql.Field{
				Type: graphql.NewList(actPromptType),
				Args: graphql.FieldConfigArgument{
					"accountId": &graphql.ArgumentConfig{Type: graphql.String},
					"groupName": &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"domain":    &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"from":      &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"to":        &graphql.ArgumentConfig{Type: graphql.String, DefaultValue: ""},
					"page":      &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 1},
					"pageSize":  &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 5},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					ctx, ok := p.Context.Value(ctxGraphQLRequest).(*graphQLRequestCtx)
					if !ok || ctx == nil {
						return nil, errGraphQLUnauthed
					}
					accountID := stringArg(p.Args, "accountId", ctx.Key.AccountID)
					if accountID != ctx.Key.AccountID {
						return nil, errGraphQLForbidden
					}
					groupName := stringArg(p.Args, "groupName", "")
					domain := apiNormDomain(stringArg(p.Args, "domain", ""))
					from, to, err := parseAPIDateRange(stringArg(p.Args, "from", ""), stringArg(p.Args, "to", ""))
					if err != nil {
						return nil, err
					}
					page := intArgGQL(p.Args, "page", 1)
					pageSize := intArgGQL(p.Args, "pageSize", apiMaxPageSize)
					if pageSize > apiMaxPageSize {
						pageSize = apiMaxPageSize
					}
					results, _, err := h.store.ListActPrompts(context.Background(), accountID, groupName, domain, from, to, page, pageSize)
					if err != nil {
						log.Printf("GQL_LIST_ACT_PROMPTS_FAILED: account=%s error=%v", accountID, err)
						return nil, err
					}
					out := make([]interface{}, len(results))
					for i, r := range results {
						out[i] = r
					}
					return out, nil
				},
			},
		},
	})

	schema, err := graphql.NewSchema(graphql.SchemaConfig{Query: queryType})
	if err != nil {
		log.Fatalf("GRAPHQL_SCHEMA_ERROR: %v", err)
	}
	kokoGraphQLSchema = schema
}

// ─────────────────────────────────────────────────────────────────────────────
// Context key for GraphQL resolver auth propagation
// ─────────────────────────────────────────────────────────────────────────────

type gqlContextKey string

const ctxGraphQLRequest gqlContextKey = "gql_request"

type graphQLRequestCtx struct {
	Key KokoAPIKey
}

var (
	errGraphQLUnauthed  = errors.New("unauthorized")
	errGraphQLForbidden = errors.New("forbidden: account mismatch")
)

// ─────────────────────────────────────────────────────────────────────────────
// POST /graphql handler
// ─────────────────────────────────────────────────────────────────────────────

type graphQLBody struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName"`
	Variables     map[string]interface{} `json:"variables"`
}

func (h *WebhookHandler) handleGraphQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	key, ok := AuthedKey(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body graphQLBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if cleanedQuery, valid := sanitizeString(body.Query); !valid || cleanedQuery == "" {
		apiError(w, http.StatusBadRequest, "query is missing or contains disallowed characters")
		return
	}

	// Inject our auth record into the resolver context
	gqlCtx := context.WithValue(r.Context(), ctxGraphQLRequest, &graphQLRequestCtx{Key: key})

	result := graphql.Do(graphql.Params{
		Schema:         kokoGraphQLSchema,
		RequestString:  body.Query,
		VariableValues: body.Variables,
		OperationName:  body.OperationName,
		Context:        gqlCtx,
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Koko-Requested-By", key.UserName)
	w.Header().Set("X-Koko-Account-ID", key.AccountID)
	w.Header().Set("X-Koko-Requested-At", time.Now().UTC().Format(time.RFC3339))

	if len(result.Errors) > 0 {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// ─────────────────────────────────────────────────────────────────────────────
// Resolver argument helpers
// ─────────────────────────────────────────────────────────────────────────────

func stringArg(args map[string]interface{}, key, def string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

func intArgGQL(args map[string]interface{}, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return def
}
