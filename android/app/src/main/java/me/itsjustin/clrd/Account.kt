package me.itsjustin.clrd

import android.content.Context
import android.content.SharedPreferences
import androidx.room.Room

/**
 * One server this phone talks to (#78): its address, token, settings file and database.
 * Account 0 is the install as it was before there could be more than one: the "clrd" file and "clrd.db",
 * so an update reads them as they are, with no copy. Choices that belong to the phone stay in [App.prefs].
 */
class Account(ctx: Context, val id: Int) {
    private val name = if (id == 0) "clrd" else "clrd-$id"
    val prefs: SharedPreferences = ctx.getSharedPreferences(name, Context.MODE_PRIVATE)
    val db by lazy { Room.databaseBuilder(ctx, Db::class.java, "$name.db").build() }

    var serverUrl: String
        get() = prefs.getString("url", "").orEmpty()
        set(v) = prefs.edit().putString("url", v.trim().trimEnd('/')).apply()
    var token: String
        get() = prefs.getString("token", "").orEmpty()
        set(v) = prefs.edit().putString("token", v.trim()).apply()

    /**
     * The server database the saved seq belongs to. Saved after the rows, not with them: a crash
     * in between only means one more reset on the next connect.
     */
    var epoch: String
        get() = prefs.getString("epoch", "").orEmpty()
        set(v) = prefs.edit().putString("epoch", v).apply()

    val configured get() = serverUrl.isNotEmpty() && token.isNotEmpty()
}
