package me.itsjustin.clrd

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

    @Query("UPDATE notif SET state = :state WHERE id = :id")
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

    /** Rows only move forward: an older snapshot never overwrites a newer one. */
    @Transaction
    fun applyAll(notifs: Collection<Notif>, seq: Long) {
        for (n in notifs) if (n.seq >= (seqOf(n.id) ?: Long.MIN_VALUE)) put(n)
        if (seq > (lastSeq() ?: 0)) putMeta(Meta("seq", seq))
    }

    @Transaction
    fun replaceAll(open: List<Notif>, seq: Long) {
        clearAll()
        open.forEach { put(it) }
        putMeta(Meta("seq", seq))
    }
}

@Database(entities = [Notif::class, Pending::class, Meta::class], version = 1)
abstract class Db : RoomDatabase() {
    abstract fun dao(): NotifDao
}
