package server

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Emoji shortcodes (#306): titles and messages are sent with common
// GitHub/Slack shortcodes such as :warning: shown as the emoji, and a tag that
// names an emoji goes in front of the title, as ntfy apps show it. Only what is
// sent changes; the notifications table keeps the text as it came in. Unknown
// names, text inside backticks and things like 10:30:15 are left as typed.
//
// ponytail: a few hundred common names, not the full gemoji list; add a name
// here when someone sends one that should work.
const emojiNames = `
+1 👍 thumbsup 👍 -1 👎 thumbsdown 👎 100 💯 1234 🔢 a 🅰️ ab 🆎 abc 🔤 airplane ✈️ alarm_clock ⏰
alien 👽 ambulance 🚑 anchor ⚓ angel 👼 anger 💢 angry 😠 ant 🐜 apple 🍎 arrow_down ⬇️
arrow_left ⬅️ arrow_right ➡️ arrow_up ⬆️ arrows_counterclockwise 🔄 art 🎨 astonished 😲
atom_symbol ⚛️ b 🅱️ baby 👶 balloon 🎈 ballot_box_with_check ☑️ bangbang ‼️ bank 🏦 bar_chart 📊
baseball ⚾ basketball 🏀 bath 🛀 battery 🔋 beer 🍺 beers 🍻 bell 🔔 bike 🚲 bird 🐦
birthday 🎂 black_circle ⚫ blue_circle 🔵 blue_heart 💙 blush 😊 bomb 💣 book 📖 bookmark 🔖
books 📚 boom 💥 bouquet 💐 bow 🙇 brain 🧠 bread 🍞 broken_heart 💔 broom 🧹 brown_circle 🟤
bug 🐛 bulb 💡 bus 🚌 cactus 🌵 cake 🍰 calendar 📆 call_me_hand 🤙 camera 📷 car 🚗
card_index 📇 cat 🐱 cd 💿 chart_with_downwards_trend 📉 chart_with_upwards_trend 📈
check ✔️ heavy_check_mark ✔️ white_check_mark ✅ checkered_flag 🏁 cherries 🍒 chicken 🐔
christmas_tree 🎄 clap 👏 clapper 🎬 clipboard 📋 clock1 🕐 closed_lock_with_key 🔐 cloud ☁️
clown_face 🤡 coffee ☕ cold_sweat 😰 collision 💥 computer 💻 confetti_ball 🎊 confused 😕
construction 🚧 cookie 🍪 cool 🆒 copyright ©️ cow 🐮 crab 🦀 credit_card 💳 crossed_fingers 🤞
crown 👑 cry 😢 crystal_ball 🔮 cupid 💘 cyclone 🌀 dart 🎯 dash 💨 date 📅 desktop_computer 🖥️
diamonds ♦️ disappointed 😞 dizzy 💫 dna 🧬 dog 🐶 dollar 💵 door 🚪 doughnut 🍩 dove 🕊️
dragon 🐉 droplet 💧 drum 🥁 duck 🦆 e-mail 📧 email 📧 ear 👂 earth_americas 🌎 egg 🥚
eight 8️⃣ envelope ✉️ euro 💶 exclamation ❗ heavy_exclamation_mark ❗ expressionless 😑
eyes 👀 face_with_head_bandage 🤕 face_with_thermometer 🤒 facepalm 🤦 fax 📠 fearful 😨 file_folder 📁
fire 🔥 fire_engine 🚒 fireworks 🎆 first_quarter_moon 🌓 fish 🐟 fist ✊ five 5️⃣ flag_white 🏳️
flashlight 🔦 floppy_disk 💾 flushed 😳 fog 🌫️ football 🏈 footprints 👣 fork_and_knife 🍴
four 4️⃣ four_leaf_clover 🍀 fox_face 🦊 frog 🐸 frowning 😦 fuelpump ⛽ full_moon 🌕 gear ⚙️
gem 💎 ghost 👻 gift 🎁 gift_heart 💝 globe_with_meridians 🌐 goat 🐐 green_apple 🍏
green_circle 🟢 green_heart 💚 grey_exclamation ❕ grey_question ❔ grimacing 😬 grin 😁
grinning 😀 guitar 🎸 gun 🔫 hamburger 🍔 hammer 🔨 hammer_and_wrench 🛠️ hand ✋ raised_hand ✋
handshake 🤝 hankey 💩 poop 💩 hash #️⃣ hatching_chick 🐣 headphones 🎧 hear_no_evil 🙉 heart ❤️
heart_eyes 😍 heartbeat 💓 heartpulse 💗 hearts ♥️ heavy_minus_sign ➖ heavy_plus_sign ➕
helicopter 🚁 herb 🌿 hibiscus 🌺 high_brightness 🔆 hocho 🔪 knife 🔪 honey_pot 🍯 horse 🐴
hospital 🏥 hotel 🏨 hourglass ⌛ hourglass_flowing_sand ⏳ house 🏠 house_with_garden 🏡
hugs 🤗 hushed 😯 ice_cream 🍨 icecream 🍦 id 🆔 inbox_tray 📥 incoming_envelope 📨
information_source ℹ️ innocent 😇 iphone 📱 jack_o_lantern 🎃 joy 😂 key 🔑 keyboard ⌨️
kiss 💋 kissing_heart 😘 koala 🐨 label 🏷️ ladybug 🐞 laptop 💻 large_blue_circle 🔵
laughing 😆 satisfied 😆 leaves 🍃 ledger 📒 lemon 🍋 link 🔗 lion 🦁 lipstick 💄 lock 🔒
lock_with_ink_pen 🔏 lollipop 🍭 loudspeaker 📢 love_letter 💌 mag 🔍 mag_right 🔎 mailbox 📫
man 👨 mask 😷 medal_sports 🏅 mega 📣 memo 📝 pencil 📝 microphone 🎤 microscope 🔬
milky_way 🌌 minibus 🚐 money_with_wings 💸 moneybag 💰 monkey 🐒 monkey_face 🐵 moon 🌔
mortar_board 🎓 mosquito 🦟 motorcycle 🏍️ mountain ⛰️ mouse 🐭 movie_camera 🎥 muscle 💪
mushroom 🍄 musical_note 🎵 nail_care 💅 nerd_face 🤓 neutral_face 😐 new 🆕 new_moon 🌑
newspaper 📰 nine 9️⃣ no_bell 🔕 no_entry ⛔ no_entry_sign 🚫 no_good 🙅 no_mouth 😶 nose 👃
notebook 📓 notes 🎶 nut_and_bolt 🔩 o ⭕ ocean 🌊 octopus 🐙 office 🏢 ok 🆗 ok_hand 👌
old_key 🗝️ one 1️⃣ open_mouth 😮 orange_circle 🟠 orange_heart 🧡 outbox_tray 📤 owl 🦉 ox 🐂
package 📦 page_facing_up 📄 palm_tree 🌴 panda_face 🐼 paperclip 📎 parking 🅿️ partly_sunny ⛅
partying_face 🥳 passport_control 🛂 paw_prints 🐾 peach 🍑 pear 🍐 penguin 🐧 pensive 😔
phone ☎️ telephone ☎️ pig 🐷 pill 💊 pin 📍 round_pushpin 📍 pineapple 🍍 pizza 🍕 point_down 👇
point_left 👈 point_right 👉 point_up ☝️ point_up_2 👆 police_car 🚓 popcorn 🍿 postbox 📮
potato 🥔 pouting_cat 😾 pray 🙏 printer 🖨️ pushpin 📌 purple_circle 🟣 purple_heart 💜
question ❓ rabbit 🐰 racing_car 🏎️ radio 📻 radio_button 🔘 rage 😡 rainbow 🌈
raised_hands 🙌 ram 🐏 recycle ♻️ red_circle 🔴 registered ®️ relaxed ☺️ relieved 😌
repeat 🔁 rewind ⏪ ribbon 🎀 ring 💍 robot 🤖 rocket 🚀 rofl 🤣 roll_eyes 🙄 rooster 🐓 rose 🌹
rotating_light 🚨 rugby_football 🏉 runner 🏃 running 🏃 sad 😞 santa 🎅 satellite 📡
saxophone 🎷 school 🏫 scissors ✂️ scream 😱 scroll 📜 seedling 🌱 see_no_evil 🙈 seven 7️⃣
shamrock ☘️ shark 🦈 sheep 🐑 shell 🐚 shield 🛡️ ship 🚢 shirt 👕 shopping_cart 🛒 shower 🚿
shrug 🤷 six 6️⃣ skull 💀 skull_and_crossbones ☠️ sleeping 😴 sleepy 😪 slightly_frowning_face 🙁
slightly_smiling_face 🙂 smile 😄 smiley 😃 smiling_imp 😈 smirk 😏 snail 🐌 snake 🐍
sneezing_face 🤧 snowflake ❄️ snowman ⛄ sob 😭 soccer ⚽ sos 🆘 sound 🔉 space_invader 👾
spades ♠️ sparkle ❇️ sparkler 🎇 sparkles ✨ sparkling_heart 💖 speak_no_evil 🙊 speaker 🔈
speech_balloon 💬 speedboat 🚤 spider 🕷️ spiral_notepad 🗒️ star ⭐ star2 🌟 stars 🌠
stethoscope 🩺 stop_button ⏹️ stop_sign 🛑 stopwatch ⏱️ strawberry 🍓 stuck_out_tongue 😛
sun_with_face 🌞 sunflower 🌻 sunglasses 😎 sunny ☀️ sunrise 🌅 sushi 🍣 sweat 😓 sweat_drops 💦
sweat_smile 😅 syringe 💉 taco 🌮 tada 🎉 tangerine 🍊 taxi 🚕 tea 🍵 ten 🔟 tennis 🎾 tent ⛺
test_tube 🧪 thermometer 🌡️ thinking 🤔 thought_balloon 💭 three 3️⃣ ticket 🎫 tiger 🐯
timer_clock ⏲️ tired_face 😫 tm ™️ toilet 🚽 tomato 🍅 tongue 👅 toolbox 🧰 tooth 🦷 top 🔝
tornado 🌪️ tractor 🚜 traffic_light 🚥 train 🚋 trident 🔱 triumph 😤 trophy 🏆 truck 🚚
trumpet 🎺 tulip 🌷 turtle 🐢 tv 📺 two 2️⃣ two_hearts 💕 umbrella ☔ unamused 😒 unicorn 🦄
unlock 🔓 up 🆙 upside_down_face 🙃 v ✌️ vertical_traffic_light 🚦 vibration_mode 📳
video_camera 📹 video_game 🎮 violin 🎻 volcano 🌋 volleyball 🏐 vomiting_face 🤮 wastebasket 🗑️
watch ⌚ watermelon 🍉 wave 👋 wavy_dash 〰️ weary 😩 whale 🐳 wheelchair ♿ white_circle ⚪
white_flag 🏳️ wilted_flower 🥀 wind_face 🌬️ wine_glass 🍷 wink 😉 wolf 🐺 woman 👩 world_map 🗺️
worried 😟 wrench 🔧 x ❌ heavy_multiplication_x ✖️ yellow_circle 🟡 yellow_heart 💛 yen 💴
yum 😋 zany_face 🤪 zap ⚡ zero 0️⃣ zipper_mouth_face 🤐 zzz 💤 warning ⚠️ no_smoking 🚭
large_orange_diamond 🔶 large_blue_diamond 🔷 small_red_triangle 🔺 red_square 🟥 green_square 🟩
yellow_square 🟨 arrow_forward ▶️ pause_button ⏸️ fast_forward ⏩ arrows_clockwise 🔃
heavy_dollar_sign 💲 chart 💹 hourglass_done ⌛ electric_plug 🔌 satellite_antenna 📡 cloud_with_rain 🌧️
cloud_with_lightning 🌩️ thunder_cloud_and_rain ⛈️ snow_cloud 🌨️ umbrella_with_rain_drops ☔
`

var emojiTable = func() map[string]string {
	f := strings.Fields(emojiNames)
	m := make(map[string]string, len(f)/2)
	for i := 0; i+1 < len(f); i += 2 {
		m[f[i]] = f[i+1]
	}
	return m
}()

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '+' || c == '-'
}

// word reports whether r is a letter or digit in any script, so é:b:ç stays as typed.
func word(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// closeFence is where the run of exactly len(fence) backticks closing a code
// span starts in s, or -1: a longer or shorter run does not close it.
func closeFence(s, fence string) int {
	for k := 0; k < len(s); {
		e := strings.Index(s[k:], fence)
		if e < 0 {
			return -1
		}
		e += k
		n := len(fence)
		for e+n < len(s) && s[e+n] == '`' {
			n++
		}
		if n == len(fence) {
			return e
		}
		k = e + n
	}
	return -1
}

// emojify shows the known shortcodes in s as emoji, outside `code` and ```blocks```.
func emojify(s string) string { out, _ := emojiScan(s); return out }

// emojiScan is emojify plus the number of shortcode-shaped names it did not
// know (with a letter, so :30: is not one), for the format check (#308).
func emojiScan(s string) (string, int) {
	unknown := 0
	if !strings.Contains(s, ":") {
		return s, 0
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' { // copy a code span or block through to its closing fence
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			if end := closeFence(s[i+n:], s[i:i+n]); end >= 0 {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
				continue
			}
			b.WriteString(s[i : i+n]) // no closing fence: plain backticks
			i += n
			continue
		}
		if before, _ := utf8.DecodeLastRuneInString(s[:i]); s[i] == ':' && !word(before) {
			j := i + 1
			for j < len(s) && nameByte(s[j]) {
				j++
			}
			if after, _ := utf8.DecodeRuneInString(s[min(j+1, len(s)):]); j < len(s) && s[j] == ':' && !word(after) {
				if e, ok := emojiTable[s[i+1:j]]; ok {
					b.WriteString(e)
					i = j + 1
					continue
				}
				if strings.ContainsAny(s[i+1:j], "abcdefghijklmnopqrstuvwxyz") {
					unknown++
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), unknown
}

// tagEmoji is the emoji of every tag that names one, as ntfy apps put in
// front of the title, and "" when none does.
func tagEmoji(tags []string) string {
	var b strings.Builder
	for _, t := range tags {
		b.WriteString(emojiTable[t])
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + " "
}

// shown is the title and message as sent: shortcodes as emoji and the tags'
// emoji in front of the title, or of the message when there is no title.
func (n Notification) shown() (title, message string) {
	title, message = emojify(n.Title), emojify(n.Message)
	if p := tagEmoji(n.Tags); p != "" {
		if title != "" {
			title = p + strings.TrimPrefix(title, p)
		} else {
			message = p + strings.TrimPrefix(message, p)
		}
	}
	return
}

// MarshalJSON sends the shown text, so every API, stream and webhook gets it.
func (n Notification) MarshalJSON() ([]byte, error) {
	type plain Notification // no methods: no recursion
	p := plain(n)
	p.Title, p.Message = n.shown()
	return json.Marshal(p)
}
