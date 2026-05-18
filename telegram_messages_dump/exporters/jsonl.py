#!/usr/bin/env python
# -*- coding: utf-8 -*-
# pylint: disable=missing-docstring

import base64
import json
from datetime import date, datetime
from telethon.tl.tlobject import TLObject
from .common import common

class jsonl(object):
    """ jsonl exporter plugin.

    As opposed to json exporter jsonl serializes messages as one JSON object per line, not as
    one giant array.

    See http://jsonlines.org.
    """
    # pylint: disable=no-self-use

    def __init__(self):
        """ constructor """
        pass

    # pylint: disable=unused-argument
    def format(self, msg, exporter_context):
        """ Formatter method. Takes raw msg and converts it to a *one-line* string.
            :param msg: Raw message object :class:`telethon.tl.types.Message` and derivatives.
                        https://core.telegram.org/type/Message

            :returns: *one-line* string containing one message data.
        """
        # pylint: disable=line-too-long
        name, _, content, re_id, is_sent_by_bot, is_contains_media, media_content = common.extract_message_data(msg)

        msgDictionary = {
            'message_id': msg.id,
            'from_id': self._get_from_id(msg),
            'reply_id': re_id,
            'author': name,
            'sent_by_bot': is_sent_by_bot,
            'date': msg.date,
            'content': content,
            'contains_media': is_contains_media,
            'media_content': media_content
        }
        msg_dump_str = json.dumps(
            msgDictionary, default=self._json_serial, ensure_ascii=False)
        return msg_dump_str

    def _get_from_id(self, msg):
        """Return a JSON-friendly sender id across Telethon versions."""
        from_id = getattr(msg, 'sender_id', None)
        if from_id is None:
            from_id = getattr(msg, 'from_id', None)
        return from_id

    def begin_final_file(self, resulting_file, exporter_context):
        """ Hook executes at the beginning of writing a resulting file.
            (After BOM is written in case of --addbom)
        """
        pass

    def _json_serial(self, obj):
        """JSON serializer for objects not serializable by default json code
           https://stackoverflow.com/questions/11875770/how-to-overcome-datetime-datetime-not-json-serializable
        """
        if isinstance(obj, (datetime, date)):
            return obj.isoformat()
        if isinstance(obj, bytes):
            return base64.b64encode(obj).decode('ascii')
        if isinstance(obj, TLObject):
            return obj.to_dict()
        raise TypeError("Type %s not serializable" % type(obj))
