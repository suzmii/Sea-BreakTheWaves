package repository

import (
	"context"
	"encoding/json"

	"recommendation_v2/internal/infrastructure"
)

// ProfileRepo 用户画像的 JSONB 存取。
type ProfileRepo struct{}

func NewProfileRepo() *ProfileRepo {
	return &ProfileRepo{}
}

// Get 返回用户完整画像。
func (r *ProfileRepo) Get(ctx context.Context, userID string) (map[string]any, error) {
	var raw []byte
	err := infrastructure.Postgres().QueryRowContext(ctx, `
		SELECT data FROM user_profiles WHERE user_id=$1
	`, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// GetOrInit 返回用户画像，不存在则返回空 map。
func (r *ProfileRepo) GetOrInit(ctx context.Context, userID string) map[string]any {
	profile, err := r.Get(ctx, userID)
	if err != nil {
		return map[string]any{
			"profile_confidence": 0.0,
		}
	}
	return profile
}

// Set 设置单个字段。
func (r *ProfileRepo) Set(ctx context.Context, userID, key string, value any) error {
	return r.SetMulti(ctx, userID, map[string]any{key: value})
}

// SetMulti 原子设置多个字段。
func (r *ProfileRepo) SetMulti(ctx context.Context, userID string, data map[string]any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = infrastructure.Postgres().ExecContext(ctx, `
		INSERT INTO user_profiles(user_id, data, updated_at)
		VALUES($1, $2::jsonb, now())
		ON CONFLICT(user_id) DO UPDATE SET
			data = user_profiles.data || $2::jsonb,
			updated_at = now()
	`, userID, string(jsonData))
	return err
}

// Delete 删除一个字段。
func (r *ProfileRepo) Delete(ctx context.Context, userID, key string) error {
	_, err := infrastructure.Postgres().ExecContext(ctx, `
		UPDATE user_profiles SET data = data - $2, updated_at = now()
		WHERE user_id=$1
	`, userID, key)
	return err
}
