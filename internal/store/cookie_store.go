package store

import "context"

type CookieStore struct {
	store *Store
	key   string
}

func NewCookieStore(store *Store, key string) *CookieStore {
	if key == "" {
		key = "115_cookie"
	}
	return &CookieStore{store: store, key: key}
}

func (c *CookieStore) Load() string {
	if c == nil || c.store == nil {
		return ""
	}
	value, ok, err := c.store.LoadKV(context.Background(), c.key)
	if err != nil || !ok {
		return ""
	}
	return value
}

func (c *CookieStore) Save(cookie string) {
	if c == nil || c.store == nil || cookie == "" {
		return
	}
	_ = c.store.SaveKV(context.Background(), c.key, cookie)
}
