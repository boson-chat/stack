/*
 * boson-react: lets the +draft/react and +draft/unreact client tags through
 * UnrealIRCd 6 (https://ircv3.net/specs/client-tags/react), the way the
 * official reply-tag module does for +draft/reply. Boson's reactions use
 * them. From the Boson setup wizard (boson-chat/stack). GPLv2+, like the
 * module it follows (src/modules/reply-tag.c).
 *
 * Install: copy this file to src/modules/third/boson-react.c in your
 * UnrealIRCd source directory, run `make && make install` there, add
 *   loadmodule "third/boson-react";
 * to unrealircd.conf and rehash.
 */

#include "unrealircd.h"

ModuleHeader MOD_HEADER = {
	"third/boson-react",
	"1.0",
	"+draft/react and +draft/unreact client tags (Boson reactions)",
	"Boson",
	"unrealircd-6",
};

int bosonreact_mtag_is_ok(Client *client, const char *name, const char *value);
void bosonreact_new_message(Client *client, MessageTag *recv_mtags, MessageTag **mtag_list, const char *signature);

static const char *tags[] = { "+draft/react", "+react", "+draft/unreact", "+unreact", NULL };

MOD_INIT()
{
	MessageTagHandlerInfo mtag;
	int i;

	for (i = 0; tags[i]; i++)
	{
		memset(&mtag, 0, sizeof(mtag));
		mtag.name = tags[i];
		mtag.is_ok = bosonreact_mtag_is_ok;
		mtag.flags = MTAG_HANDLER_FLAGS_NO_CAP_NEEDED | MTAG_HANDLER_FLAGS_FIRST_ONLY;
		MessageTagHandlerAdd(modinfo->handle, &mtag);
	}
	HookAddVoid(modinfo->handle, HOOKTYPE_NEW_MESSAGE, 0, bosonreact_new_message);
	return MOD_SUCCESS;
}

MOD_LOAD()
{
	return MOD_SUCCESS;
}

MOD_UNLOAD()
{
	return MOD_SUCCESS;
}

/* A reaction is a short text (an emoji or a short code). */
int bosonreact_mtag_is_ok(Client *client, const char *name, const char *value)
{
	if (BadPtr(value))
		return 0;
	if (strlen(value) > 64)
		return 0;
	return 1;
}

void bosonreact_new_message(Client *client, MessageTag *recv_mtags, MessageTag **mtag_list, const char *signature)
{
	MessageTag *m;
	int i;

	if (!IsUser(client))
		return;
	for (i = 0; tags[i]; i++)
	{
		m = find_mtag(recv_mtags, tags[i]);
		if (m)
		{
			m = duplicate_mtag(m);
			AddListItem(m, *mtag_list);
		}
	}
}
