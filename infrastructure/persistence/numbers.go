package persistence

import "gorm.io/gorm"

// nextNumber takes the repository's next issue/pull request number, inside
// tx. Pull requests and issues share it, so #12 is one or the other. The
// UPDATE locks the repository row (Postgres) or the database (SQLite)
// until tx ends, so concurrent creates get different numbers; the unique
// (repository_id, number) indexes back it up.
func nextNumber(tx *gorm.DB, repositoryId string) (int, error) {
	res := tx.Exec("UPDATE repository SET last_number = last_number + 1 WHERE id = ?", repositoryId)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected != 1 {
		return 0, gorm.ErrRecordNotFound
	}
	var n int
	err := tx.Raw("SELECT last_number FROM repository WHERE id = ?", repositoryId).Scan(&n).Error
	return n, err
}
