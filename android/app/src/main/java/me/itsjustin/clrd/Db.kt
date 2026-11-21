package me.itsjustin.clrd

import androidx.room.AutoMigration
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.RoomDatabase
import androidx.room.Transaction
import kotlinx.coroutines.flow.Flow

@Dao
interface NotifDao {
    @Query("SELECT * FROM notif WHERE state IN ('active','acknowledged') ORDER BY createdAt DESC")
    fun inbox(): Flow<List<Notif>>

    @Query("SELECT * FROM notif WHERE state = 'snoozed' ORDER BY snoozeUntil")
    fun snoozed(): Flow<List<Notif>>

    @Query("SELECT * FROM notif WHERE state IN ('dismissed','resolved') ORDER BY updatedAt DESC LIMIT 200")
    fun cleared(): Flow<List<Notif>>

    @Query("SELECT * FROM notif WHERE state IN ('active','acknowledged','snoozed')")
    fun openNow(): List<Notif>

    @Query("SELECT DISTINCT topic FROM notif ORDER BY topic")
    fun topics(): Flow<List<String>>

    @Query("SELECT seq FROM notif WHERE id = :id")
    fun seqOf(id: String): Long?

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    fun put(n: Notif)

    // Only Reopen brings back a cleared row. A tap that raced a dismiss or resolve must not, since the server then refuses it.
    @Query("UPDATE notif SET state = :state WHERE id = :id AND (:state = 'active' OR state NOT IN ('dismissed', 'resolved'))")
    fun setState(id: String, state: String)

    @Query("DELETE FROM notif")
    fun clearAll()

    /** Final rows are history only; keep a week of it. */
    @Query("DELETE FROM notif WHERE state IN ('dismissed','resolved') AND updatedAt < :before")
    fun prune(before: Long)

    @Query("SELECT v FROM meta WHERE k = 'seq'")
    fun lastSeq(): Long?

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    fun putMeta(m: Meta)

    @Query("SELECT * FROM pending ORDER BY `key`")
    fun pending(): List<Pending>

    @Insert
    fun addPending(p: Pending)

    @Query("DELETE FROM pending WHERE `key` = :key")
    fun removePending(key: Long)

    /** Rows only move forward: an older snapshot never overwrites a newer one. Returns the rows kept. */
    @Transaction
    fun applyAll(notifs: Collection<Notif>, seq: Long): List<Notif> {
        val kept = notifs.filter { n -> n.seq >= (seqOf(n.id) ?: Long.MIN_VALUE) }
        kept.forEach { put(it) }
        if (seq > (lastSeq() ?: 0)) putMeta(Meta("seq", seq))
        return kept
    }

    @Query("DELETE FROM pending")
    fun clearPending()

    /** A different server or token: nothing cached belongs to it. */
    @Transaction
    fun forget() {
        clearAll()
        clearPending()
        putMeta(Meta("seq", 0))
    }

    @Query("SELECT * FROM notif WHERE id = :id")
    fun get(id: String): Notif?

    /**
     * The open list replaces everything, except a listed row Room already has in a newer version
     * (an action answer saved while the list was on its way). Returns the rows now stored.
     * A list behind our cursor means the server was wiped or restored: then nothing old is kept.
     * The same goes for a list from another database ([sameDb] false), whatever its seq.
     */
    @Transaction
    fun replaceAll(open: List<Notif>, seq: Long, sameDb: Boolean = true): List<Notif> {
        val keepNewer = sameDb && seq >= (lastSeq() ?: 0)
        val rows = open.map { n -> get(n.id)?.takeIf { keepNewer && it.seq > n.seq } ?: n }
        clearAll()
        rows.forEach { put(it) }
        putMeta(Meta("seq", seq))
        return rows
    }
}

// Room writes each version to schemas/; AutoMigration keeps the queue and the cursor across an app update.
@Database(entities = [Notif::class, Pending::class, Meta::class], version = 5, autoMigrations = [AutoMigration(from = 1, to = 2), AutoMigration(from = 2, to = 3), AutoMigration(from = 3, to = 4), AutoMigration(from = 4, to = 5)])
abstract class Db : RoomDatabase() {
    abstract fun dao(): NotifDao
}
