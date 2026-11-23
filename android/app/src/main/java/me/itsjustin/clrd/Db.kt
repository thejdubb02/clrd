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

    /** A snooze shown at once with its time (#473). [reopen] lets an Undo of a Clear bring a cleared row back snoozed. */
    @Query("UPDATE notif SET state = 'snoozed', snoozeUntil = :until WHERE id = :id AND (:reopen OR state NOT IN ('dismissed', 'resolved'))")
    fun snooze(id: String, until: Long, reopen: Boolean)

    @Query("DELETE FROM notif")
    fun clearAll()

    /** Final rows are history only: drop those cleared before [before], outside the topics in [skip]. */
    @Query("DELETE FROM notif WHERE state IN ('dismissed','resolved') AND updatedAt < :before AND topic NOT IN (:skip)")
    fun prune(before: Long, skip: List<String>)

    /**
     * Every cleared row, whatever its age or topic (#151), once it is safe to forget: the stream cursor has passed
     * its seq, so no older event can bring it back, and nothing is waiting to send, so an offline Undo still finds it.
     */
    @Query("DELETE FROM notif WHERE state IN ('dismissed','resolved') AND seq <= (SELECT v FROM meta WHERE k = 'seq') AND NOT EXISTS (SELECT 1 FROM pending)")
    fun dropCleared()

    @Query("DELETE FROM notif WHERE state IN ('dismissed','resolved') AND updatedAt < :before AND topic = :topic")
    fun pruneTopic(topic: String, before: Long)

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
    fun applyAll(notifs: Collection<Notif>, seq: Long, answer: Boolean = false): List<Notif> {
        val kept = notifs.filter { n -> n.seq >= (seqOf(n.id) ?: Long.MIN_VALUE) }
        kept.forEach { put(it) }
        // [answer]: these rows are this phone's own change. Kept even when a newer row got in first: a repeat older
        // than the change was still sent before it (#561 review).
        if (answer) notifs.forEach { answered(it.id, it.seq) }
        if (seq > (lastSeq() ?: 0)) putMeta(Meta("seq", seq))
        return kept
    }

    @Query("SELECT seq FROM answer WHERE id = :id")
    fun answerOf(id: String): Long?

    @Query("INSERT OR REPLACE INTO answer (id, seq) VALUES (:id, MAX(:seq, COALESCE((SELECT seq FROM answer WHERE id = :id), 0)))")
    fun answered(id: String, seq: Long)

    @Query("DELETE FROM answer")
    fun clearAnswers()

    /** An own change is only needed while its notification is kept: a repeat for a row Room no longer has never rings again. */
    @Query("DELETE FROM answer WHERE id NOT IN (SELECT id FROM notif)")
    fun pruneAnswers()

    @Query("DELETE FROM pending")
    fun clearPending()

    /** A different server or token: nothing cached belongs to it. */
    @Transaction
    fun forget() {
        clearAll()
        clearPending()
        clearAnswers()
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
        if (keepNewer) pruneAnswers() else clearAnswers() // a restored or other database counts seqs anew
        putMeta(Meta("seq", seq))
        return rows
    }
}

// Room writes each version to schemas/; AutoMigration keeps the queue and the cursor across an app update.
@Database(entities = [Notif::class, Pending::class, Meta::class, Answer::class], version = 8, autoMigrations = [AutoMigration(from = 1, to = 2), AutoMigration(from = 2, to = 3), AutoMigration(from = 3, to = 4), AutoMigration(from = 4, to = 5), AutoMigration(from = 5, to = 6), AutoMigration(from = 6, to = 7), AutoMigration(from = 7, to = 8)])
abstract class Db : RoomDatabase() {
    abstract fun dao(): NotifDao
}
