import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

import requests


class AppTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.import_dir = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.import_dir.cleanup)
        source = Path(__file__).resolve().parents[1] / 'app.py'
        spec = importlib.util.spec_from_file_location('office365_test_app', source)
        cls.module = importlib.util.module_from_spec(spec)
        with patch.dict(os.environ, {'CONFIG_FILE': str(Path(cls.import_dir.name) / 'config.json')}), \
                patch.object(sys, 'dont_write_bytecode', True):
            spec.loader.exec_module(cls.module)
        cls.module.app.root_path = str(source.parent)
        cls.module.app.config.update(TESTING=True, SECRET_KEY='test-session-key')

    def setUp(self):
        self.m = self.module
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.org = {'id': 'org-a', 'name': 'Test tenant', 'client_id': 'test-client',
                    'tenant_id': 'test-tenant', 'client_secret': 'test-only-secret'}
        self.config_path = Path(self.temp.name) / 'config.json'
        for name, value in {
            'CONFIG_FILE': str(self.config_path),
            'config': {'password_hash': 'test-hash', 'organizations': [self.org]},
            'token_cache': {}, 'org_api_status': {}, '_role_cache': {},
            'user_paging_cache': {}, 'summary_cache': {}, '_summary_versions': {},
            '_http_local': threading.local(),
        }.items():
            p = patch.object(self.m, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.m.save_config(self.m.config)
        p = patch.object(requests.Session, 'request', side_effect=AssertionError('Unexpected network request'))
        self.network = p.start()
        self.addCleanup(p.stop)
        p = patch.object(self.m, 'ConfidentialClientApplication')
        self.msal = p.start()
        self.msal.return_value.acquire_token_for_client.return_value = {'access_token': 'test-token'}
        self.addCleanup(p.stop)
        self.client = self.m.app.test_client()
        with self.client.session_transaction() as session:
            session['logged_in'] = True
            session['org_id'] = self.org['id']

    def graph_data(self, org, method, endpoint, **kwargs):
        if endpoint == '/users/$count':
            return '12'
        if endpoint == '/roleManagement/directory/roleDefinitions':
            return {'value': [{'id': kwargs['params']['$filter']}]}
        if endpoint == '/users':
            return {'value': [{'id': 'user-a', 'displayName': 'Test User', 'userPrincipalName': 'test@example.invalid'}]}
        return {'value': []}

    def test_login_logout_and_protected_routes(self):
        self.client.get('/logout')
        self.assertEqual(self.client.get('/users').status_code, 302)
        self.assertEqual(self.client.get('/login').status_code, 200)
        with patch.object(self.m, 'verify_password', return_value=True):
            self.assertEqual(self.client.post('/login', data={'password': 'test'}).status_code, 302)
        self.assertEqual(self.client.get('/organizations').status_code, 200)

    def test_token_timeout_and_cache(self):
        self.assertEqual(self.m.get_access_token(self.org), 'test-token')
        self.assertEqual(self.msal.call_args.kwargs['timeout'], (5, 15))
        self.assertEqual(self.m.get_access_token(self.org), 'test-token')
        self.assertEqual(self.msal.call_count, 1)

    def test_token_failures_do_not_reach_graph(self):
        for error in (requests.ConnectTimeout, requests.ReadTimeout, ValueError):
            with self.subTest(error=error):
                self.msal.side_effect = error('private-data')
                self.assertIsNone(self.m.graph_request(self.org, 'GET', '/users'))
        self.network.assert_not_called()

    def test_sessions_are_reused_per_thread(self):
        session = self.m.get_http_session()
        self.addCleanup(session.close)
        self.assertIs(session, self.m.get_http_session())
        sessions = []
        thread = threading.Thread(target=lambda: sessions.append(self.m.get_http_session()))
        thread.start()
        thread.join()
        self.addCleanup(sessions[0].close)
        self.assertIsNot(session, sessions[0])

    def test_graph_timeout_headers_and_no_write_retries(self):
        self.network.side_effect = requests.ReadTimeout('secret-in-exception')
        self.m.summary_cache['org-a'] = {'summary': {}}
        with self.assertLogs(self.m.app.logger, level='INFO') as logs:
            self.assertIsNone(self.m.graph_request(self.org, 'POST', '/users/private-id', data={'private': 'data'}))
        self.assertEqual(self.network.call_count, 1)
        self.assertEqual(self.network.call_args.kwargs['timeout'], (5, 15))
        self.assertNotIn('org-a', self.m.summary_cache)
        self.assertNotIn('private-id', '\n'.join(logs.output))
        self.assertNotIn('secret-in-exception', '\n'.join(logs.output))
        self.network.side_effect = requests.ConnectTimeout
        self.assertIsNone(self.m.graph_request(self.org, 'GET', '/users'))

    def test_graph_response_contract(self):
        for status, body, expected in (
            (200, {'value': []}, {'value': []}),
            (204, None, {}),
            (403, {'error': {'message': 'denied'}}, {'error': {'message': 'denied'}}),
            (429, {'error': {'message': 'limited'}}, {'error': {'message': 'limited'}}),
            (500, {'unexpected': True}, None),
        ):
            with self.subTest(status=status):
                self.network.side_effect = None
                self.network.return_value = Mock(status_code=status, json=Mock(return_value=body))
                self.assertEqual(self.m.graph_request(self.org, 'GET', '/users'), expected)
        self.network.return_value = Mock(status_code=200, text='42\n')
        self.assertEqual(self.m.graph_request(self.org, 'GET', '/users/$count'), '42')
        self.network.return_value = Mock(status_code=200, json=Mock(side_effect=ValueError))
        self.assertIsNone(self.m.graph_request(self.org, 'GET', '/users'))

    def test_401_invalidates_token(self):
        self.network.side_effect = None
        self.network.return_value = Mock(status_code=401)
        self.assertIsNone(self.m.graph_request(self.org, 'GET', '/users'))
        self.assertNotIn('org-a', self.m.token_cache)

    def test_summary_cache_refresh_expiry_and_organization_isolation(self):
        with patch.object(self.m, 'graph_request', side_effect=self.graph_data) as graph:
            self.assertEqual(self.client.get('/users').status_code, 200)
            self.assertEqual(graph.call_count, 7)
            self.client.get('/users')
            self.assertEqual(graph.call_count, 7)
            self.client.get('/users?refresh=1')
            self.assertEqual(graph.call_count, 12)
            self.m.summary_cache['org-a']['expires_at'] = 0
            self.client.get('/users')
            self.assertEqual(graph.call_count, 17)
            self.m.config['organizations'].append(dict(self.org, id='org-b'))
            with self.client.session_transaction() as session:
                session['org_id'] = 'org-b'
            self.client.get('/users')
            self.assertEqual(graph.call_count, 24)

    def test_partial_summary_is_not_cached_or_reported_as_zero(self):
        def failed_roles(org, method, endpoint, **kwargs):
            if endpoint.endswith('/roleAssignments'):
                return None
            return self.graph_data(org, method, endpoint, **kwargs)
        with patch.object(self.m, 'graph_request', side_effect=failed_roles):
            response = self.client.get('/users')
        self.assertEqual(response.status_code, 200)
        self.assertIn('部分统计', response.text)
        self.assertIn('class="stat-number">—', response.text)
        self.assertNotIn('org-a', self.m.summary_cache)

    def test_optional_subscription_failure_is_not_cached(self):
        def failed_subscription(org, method, endpoint, **kwargs):
            if endpoint == '/directory/subscriptions':
                return {'error': {'message': 'denied'}}
            return self.graph_data(org, method, endpoint, **kwargs)
        with patch.object(self.m, 'graph_request', side_effect=failed_subscription):
            self.assertEqual(self.client.get('/users').status_code, 200)
        self.assertNotIn('org-a', self.m.summary_cache)

    def test_summary_does_not_repopulate_after_background_write(self):
        def mutating_graph(org, method, endpoint, **kwargs):
            if endpoint == '/users/$count':
                self.m.invalidate_org_cache(org['id'])
            return self.graph_data(org, method, endpoint, **kwargs)
        with patch.object(self.m, 'graph_request', side_effect=mutating_graph):
            self.assertEqual(self.client.get('/users').status_code, 200)
        self.assertNotIn('org-a', self.m.summary_cache)

    def test_error_responses_never_mark_api_available(self):
        for response in (None, {'error': {'message': 'denied'}}, {}):
            with self.subTest(response=response), patch.object(self.m, 'graph_request', return_value=response):
                self.client.get('/organizations/test/org-a')
                self.assertFalse(self.m.org_api_status['org-a'])
                self.client.get('/organizations/test_all')
                self.assertFalse(self.m.org_api_status['org-a'])
                self.assertIn('API 不可用', self.client.get('/users').text)

    def test_list_role_failure_is_unknown_not_normal(self):
        def failed_roles(org, method, endpoint, **kwargs):
            if endpoint.endswith('/roleAssignments'):
                return None
            return self.graph_data(org, method, endpoint, **kwargs)
        with patch.object(self.m, 'graph_request', side_effect=failed_roles):
            response = self.client.get('/users?view=users')
        self.assertEqual(response.status_code, 200)
        self.assertIn('角色信息暂不可用', response.text)

    def test_role_filtered_list_failure_does_not_raise(self):
        with patch.object(self.m, 'get_role_definition_id', return_value='role'), \
                patch.object(self.m, 'graph_request', return_value=None):
            response = self.client.get('/users?view=users&role=Global+Administrator')
        self.assertEqual(response.status_code, 200)
        self.assertIn('API 不可用', response.text)

    def test_failed_page_is_not_cached_as_empty(self):
        with patch.object(self.m, 'graph_request', return_value={'error': {'message': 'limited'}}):
            response = self.client.get('/users?view=users')
        self.assertIn('API 不可用', response.text)
        self.assertEqual(next(iter(self.m.user_paging_cache.values()))['pages'], [])

    def test_all_graph_writes_invalidate_only_affected_organization(self):
        self.network.side_effect = None
        self.network.return_value = Mock(status_code=204)
        for method in ('POST', 'PATCH', 'DELETE'):
            with self.subTest(method=method):
                self.m.summary_cache.update({'org-a': {}, 'org-b': {}})
                self.m.user_paging_cache.update({('org-a', '', '', 50): {}, ('org-b', '', '', 50): {}})
                thread = threading.Thread(target=self.m.graph_request, args=(self.org, method, '/users/user-a'))
                thread.start()
                thread.join()
                self.assertNotIn('org-a', self.m.summary_cache)
                self.assertIn('org-b', self.m.summary_cache)
                self.assertNotIn(('org-a', '', '', 50), self.m.user_paging_cache)

    def test_edit_organization_invalidates_token_and_summary(self):
        self.m.token_cache['org-a'] = {'access_token': 'old'}
        self.m.summary_cache['org-a'] = {}
        self.m._role_cache[('org-a', 'Global Administrator')] = 'role'
        response = self.client.post('/organizations/edit/org-a', data=self.org)
        self.assertEqual(response.status_code, 302)
        self.assertNotIn('org-a', self.m.token_cache)
        self.assertNotIn('org-a', self.m.summary_cache)
        self.assertFalse(self.m._role_cache)
        self.assertEqual(json.loads(self.config_path.read_text())['organizations'][0]['id'], 'org-a')

    def test_delete_organization_invalidates_summary(self):
        self.m.summary_cache['org-a'] = {}
        self.client.get('/organizations/delete/org-a')
        self.assertNotIn('org-a', self.m.summary_cache)
        self.assertEqual(self.m.load_config()['organizations'], [])

    def test_invalid_config_is_preserved(self):
        for content in ('', '{broken', '[]', '{}', '{"password_hash":"x","organizations":[{}]}'):
            with self.subTest(content=content):
                self.config_path.write_text(content)
                with self.assertRaisesRegex(ValueError, '原文件未修改'):
                    self.m.load_config()
                self.assertEqual(self.config_path.read_text(), content)

    def test_new_config_directory_and_reload(self):
        path = Path(self.temp.name) / 'data' / 'config.json'
        with patch.object(self.m, 'CONFIG_FILE', str(path)):
            config = self.m.load_config()
            self.assertEqual(config['organizations'], [])
            config['organizations'] = [copy.deepcopy(self.org)]
            self.m.save_config(config)
            self.assertEqual(self.m.load_config(), config)

    def test_failed_license_read_prevents_mutation(self):
        with patch.object(self.m, 'graph_request', return_value=None) as graph:
            response = self.client.post('/user/user-a/license', data={'sku_ids': 'sku-a'})
        self.assertEqual(response.status_code, 302)
        self.assertEqual(graph.call_count, 1)
        self.assertEqual(graph.call_args.args[1], 'GET')

    def test_failed_role_write_preserves_existing_other_role(self):
        self.m._role_cache.update({('org-a', 'Global Administrator'): 'ga',
                                   ('org-a', 'Privileged Role Administrator'): 'pra'})
        with patch.object(self.m, 'graph_request', side_effect=[
            {'value': [{'id': 'old-assignment', 'roleDefinitionId': 'pra'}]}, None,
        ]) as graph:
            response = self.client.post('/user/user-a/update', data={'role': 'Global Administrator'})
        self.assertEqual(response.status_code, 302)
        self.assertEqual([call.args[1] for call in graph.call_args_list], ['GET', 'POST'])
        with self.client.session_transaction() as session:
            messages = str(session.get('_flashes'))
        self.assertIn('角色更新未完成', messages)
        self.assertNotIn('已设置为全局管理员', messages)

    def test_failed_role_read_prevents_mutation(self):
        self.m._role_cache.update({('org-a', 'Global Administrator'): 'ga',
                                   ('org-a', 'Privileged Role Administrator'): 'pra'})
        with patch.object(self.m, 'graph_request', return_value=None) as graph:
            response = self.client.post('/user/user-a/update', data={'role': 'normal'})
        self.assertEqual(response.status_code, 302)
        self.assertEqual(graph.call_count, 1)
        self.assertEqual(graph.call_args.args[1], 'GET')

    def test_error_without_message_is_not_success(self):
        with patch.object(self.m, 'graph_request', return_value={'error': {'code': 'Denied'}}):
            self.client.post('/user/user-a/update', data={'account_enabled': 'false'})
        with self.client.session_transaction() as session:
            messages = str(session.get('_flashes'))
        self.assertIn('失败', messages)
        self.assertNotIn('账户启用状态已更新', messages)

    def test_summary_auth_timeout_returns_error_page(self):
        self.msal.side_effect = requests.ReadTimeout
        response = self.client.get('/users')
        self.assertEqual(response.status_code, 200)
        self.assertIn('API 不可用', response.text)
        self.network.assert_not_called()


if __name__ == '__main__':
    unittest.main()
